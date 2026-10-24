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
      mkdir inputs
      printf "archive payload\n" > inputs/file.txt
      printf "second payload\n" > inputs/second.txt
      touch -t 202009131226.40 inputs/file.txt
      7z a -tzip UPPER.ZIP ./inputs/file.txt >/dev/null
      7z a -t7z -ms=on -mhe=on -pfixture-password encrypted.7z ./inputs >/dev/null
      7z a -ttar bundle.tar ./inputs >/dev/null
      7z a -tgzip bundle.tar.gz bundle.tar >/dev/null
      7z a -twim image.wim ./inputs >/dev/null
      cp UPPER.ZIP protected.zip
      rm -rf inputs bundle.tar
      mkdir UPPER
      printf "existing destination\n" > UPPER/existing.txt
    '
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
  test "$(cat "$data/image/inputs/file.txt")" = 'archive payload'
  test "$(stat -c '%u:%g' "$data/UPPER (1)/file.txt")" = "$(id -u):$(id -g)"
  for extracted in 'UPPER (1)/file.txt' encrypted/inputs/file.txt bundle/inputs/file.txt image/inputs/file.txt; do
    test "$(stat -c '%Y' "$data/$extracted")" = 1600000000
  done
  test -f "$data/protected.zip"
  for archive in UPPER.ZIP encrypted.7z bundle.tar.gz image.wim; do
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
echo 'Extract passed dry-run, read-only volumes, passwords, compound archives, WIM, collisions and corrupt archive preservation.'
