#!/usr/bin/env bash
set -euo pipefail

: "${LOCAL_IMAGE:?Set LOCAL_IMAGE to the built Docker image}"
fixture="$(mktemp -d)"
trap 'rm -rf -- "$fixture"' EXIT

snapshot() {
  (
    cd "$1"
    find . -printf '%P %y\n' | sort
    find . -type f -print0 | sort -z | xargs -0 sha256sum
  )
}

for run_user in 0:0 "$(id -u):$(id -g)"; do
  data="$fixture/${run_user/:/-}"
  mkdir -p "$data"
  docker run --rm --network none --read-only \
    --user "$(id -u):$(id -g)" \
    --mount "type=bind,src=$data,dst=/data" \
    --entrypoint /bin/sh "$LOCAL_IMAGE" -ec '
      cd /data
      mkdir -p inputs/empty
      printf "archive payload\n" > inputs/file.txt
      printf "second payload\n" > inputs/second.txt
      touch -t 202009131226.40 inputs/file.txt
      cp inputs/file.txt inputs/shared-copy.txt
      7z a -tzip UPPER.ZIP ./inputs/file.txt >/dev/null
      7z a -t7z -ms=on -mhe=on -pfixture-password encrypted.7z ./inputs >/dev/null
      7z a -ttar bundle.tar ./inputs >/dev/null
      7z a -tgzip bundle.tar.gz bundle.tar >/dev/null
      7z a -txz bundle-xz.tar.xz bundle.tar >/dev/null
      7z a -tbzip2 bundle-bz2.tbz2 bundle.tar >/dev/null
      7z a -txz stream-xz.xz ./inputs/file.txt >/dev/null
      7z a -tbzip2 stream-bz2.bz2 ./inputs/file.txt >/dev/null
      printf %s KLUv/SAOcQAAc3RyZWFtIHBheWxvYWQ= | base64 -d > stream-zstd.zst
      printf %s XQAAgAD//////////wA5nQqMjIpEnC/AAXQiamqHMQP//4AaAAA= | base64 -d > stream-lzma.lzma
      7z a -twim image.wim ./inputs >/dev/null
      cp UPPER.ZIP protected.zip
      rm -rf inputs bundle.tar
      mkdir UPPER
      printf "existing destination\n" > UPPER/existing.txt
    '
  python3 - "$data" <<'PY'
import os
import sys
import zipfile

with zipfile.ZipFile(os.path.join(sys.argv[1], 'long-names.zip'), 'w') as archive:
    archive.writestr('界' * 90 + 'a.txt', 'first long file')
    archive.writestr('界' * 90 + 'b.txt', 'second long file')
    archive.writestr('界' * 83 + '.txt', 'unchanged original name')
    archive.writestr('目' * 90 + '/child.txt', 'child of long directory')

for entry in os.scandir(sys.argv[1]):
    if entry.is_file():
        os.setxattr(entry.path, 'user.dedup-extract-regression', b'archive download metadata')
PY
  before="$(snapshot "$data")"
  docker run --rm --network none --read-only --user "$run_user" \
    --mount "type=bind,src=$data,dst=/dedup,readonly" \
    -e MODE=Extract -e ROOTS=/dedup -e TARGET=/ignored \
    -e PASSWORDS=wrong-password,fixture-password -e IGNORE_KEYWORDS=protected "$LOCAL_IMAGE"
  test "$before" = "$(snapshot "$data")"

  if docker run --rm --network none --read-only --user "$run_user" \
    --mount "type=bind,src=$data,dst=/dedup,readonly" \
    -e MODE=Extract -e ROOTS=/dedup -e DRY_RUN=false "$LOCAL_IMAGE"; then
    echo 'Extract unexpectedly succeeded on a read-only volume.' >&2
    exit 1
  fi
  test "$before" = "$(snapshot "$data")"

  capabilities=()
  if [[ "$run_user" != 0:0 ]]; then
    capabilities+=(--cap-drop ALL)
  fi
  logs="$(docker run --rm --network none --read-only "${capabilities[@]}" \
    --user "$run_user" \
    --mount "type=bind,src=$data,dst=/dedup" \
    -e MODE=Extract -e ROOTS=/dedup -e TARGET=/ignored -e DRY_RUN=false \
    -e TZ=Asia/Shanghai \
    -e PASSWORDS=wrong-password,fixture-password -e IGNORE_KEYWORDS=protected "$LOCAL_IMAGE" 2>&1)"
  printf '%s\n' "$logs"
  if grep -Eq 'fixture-password|wrong-password' <<< "$logs"; then
    echo 'A password appeared in extraction logs.' >&2
    exit 1
  fi
  test "$(cat "$data/UPPER/existing.txt")" = 'existing destination'
  test "$(cat "$data/UPPER (1)/file.txt")" = 'archive payload'
  test "$(cat "$data/encrypted/inputs/second.txt")" = 'second payload'
  test "$(cat "$data/bundle/inputs/file.txt")" = 'archive payload'
  test "$(cat "$data/bundle-xz/inputs/file.txt")" = 'archive payload'
  test "$(cat "$data/bundle-bz2/inputs/file.txt")" = 'archive payload'
  for format in xz bz2; do
    test "$(cat "$data/stream-$format/stream-$format")" = 'archive payload'
  done
  for format in zstd lzma; do
    test "$(cat "$data/stream-$format/stream-$format")" = 'stream payload'
  done
  for extracted in encrypted bundle bundle-xz bundle-bz2 image; do
    test -d "$data/$extracted/inputs/empty"
  done
  test "$(cat "$data/image/inputs/file.txt")" = 'archive payload'
  test "$(cat "$data/image/inputs/shared-copy.txt")" = 'archive payload'
  python3 - "$data/long-names" <<'PY'
import os
import sys

expected = {
    '界' * 83 + '.txt': 'unchanged original name',
    '界' * 82 + ' (1).txt': 'first long file',
    '界' * 82 + ' (2).txt': 'second long file',
    '目' * 85 + '/child.txt': 'child of long directory',
}
for name, content in expected.items():
    with open(os.path.join(sys.argv[1], name), encoding='utf-8') as output:
        assert output.read() == content
PY
  test "$(stat -c '%u:%g' "$data/UPPER (1)/file.txt")" = "$(id -u):$(id -g)"
  for extracted in 'UPPER (1)/file.txt' encrypted/inputs/file.txt bundle/inputs/file.txt image/inputs/file.txt; do
    test "$(stat -c '%Y' "$data/$extracted")" = 1600000000
  done
  test -f "$data/protected.zip"
  python3 - "$data/protected.zip" <<'PY'
import os
import sys

assert os.getxattr(sys.argv[1], 'user.dedup-extract-regression') == b'archive download metadata'
PY
  for archive in UPPER.ZIP encrypted.7z bundle.tar.gz bundle-xz.tar.xz bundle-bz2.tbz2 image.wim long-names.zip stream-xz.xz stream-bz2.bz2 stream-zstd.zst stream-lzma.lzma; do
    test ! -e "$data/$archive"
  done
  test -z "$(find "$data" -name '.dedup-*' -print -quit)"
done

data="$fixture/corrupt"
mkdir -p "$data"
printf 'broken archive' > "$data/broken.zip"
before="$(snapshot "$data")"
if docker run --rm --network none --read-only \
  --mount "type=bind,src=$data,dst=/dedup" \
  -e MODE=Extract -e DRY_RUN=false "$LOCAL_IMAGE"; then
  echo 'Corrupt archive unexpectedly succeeded.' >&2
  exit 1
fi
test "$before" = "$(snapshot "$data")"

for run_user in 0:0 "$(id -u):$(id -g)"; do
  data="$fixture/continue-${run_user/:/-}"
  mkdir -p "$data"
  printf 'broken archive' > "$data/a-broken.zip"
  original="$(sha256sum "$data/a-broken.zip")"
  python3 - "$data/z-valid.zip" <<'PY'
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1], 'w') as archive:
    archive.writestr('payload.txt', 'next archive payload')
PY
  if logs="$(docker run --rm --network none --read-only --user "$run_user" \
    --mount "type=bind,src=$data,dst=/dedup" \
    -e MODE=Extract -e DRY_RUN=false "$LOCAL_IMAGE" 2>&1)"; then
    echo 'A partial extraction failure unexpectedly returned success.' >&2
    exit 1
  fi
  printf '%s\n' "$logs"
  grep -Fq 'Skipping archive' <<< "$logs"
  grep -Fq '1 archives extracted' <<< "$logs"
  grep -Fq '1 archives failed' <<< "$logs"
  test "$original" = "$(sha256sum "$data/a-broken.zip")"
  test "$(cat "$data/z-valid/payload.txt")" = 'next archive payload'
  test ! -e "$data/z-valid.zip"
  test -z "$(find "$data" -name '.dedup-*' -print -quit)"
done

echo 'Extract passed extended attributes, dry-run, read-only volumes, passwords, compressed streams, compound archives, empty directories, WIM shared data, long Unicode names, collisions, corrupt archive preservation and continuation after failures.'
