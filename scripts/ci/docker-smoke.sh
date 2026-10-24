#!/usr/bin/env bash
set -euo pipefail

: "${LOCAL_IMAGE:?Set LOCAL_IMAGE to the built Docker image}"

fixture="$(mktemp -d)"
trap 'rm -rf -- "$fixture"' EXIT

snapshot() {
  (
    cd "$1"
    find . -printf '%P %y\n' | sort
    find . -type l -printf '%P -> %l\n' | sort
    find . -type f -print0 | sort -z | xargs -0 sha256sum
  )
}

run_cleanup() {
  docker run --rm --network none --read-only \
    --workdir /dedup \
    --user "$run_user" \
    --mount "type=bind,src=$data,dst=/dedup$1" \
    -e MODE="$mode" -e LOG=debug -e EMPTY_DIR=true -e MIN_SIZE=4 -e EXCLUDE_EXT=.skip \
    -e DELETE_FILES_INCLUDING=delete-me,remove-me -e IGNORE_KEYWORDS=protected \
    "${@:2}" "$LOCAL_IMAGE"
}

for run_user in 0:0 "$(id -u):$(id -g)"; do
for mode in dedup delete-files-including flatten; do
  data="$fixture/${run_user/:/-}/$mode/data"
  expected="$fixture/${run_user/:/-}/$mode/expected"
  mkdir -p "$data/child/nested" "$data/empty/nested" "$data/metadata" "$expected"
  printf 'duplicate content\n' > "$data/a-original.txt"
  cp "$data/a-original.txt" "$data/child/nested/longer-duplicate-name.txt"
  printf 'unique content to preserve\n' > "$data/keep.txt"
  printf 'delete by filename\n' > "$data/delete-me.tmp"
  printf 'second matching filename\n' > "$data/remove-me.tmp"
  printf 'keep because of ignored keyword\n' > "$data/protected-delete-me.tmp"
  printf 'excluded duplicate content\n' > "$data/first.skip"
  cp "$data/first.skip" "$data/child/nested/second.skip"
  printf 'x' > "$data/small-delete-me.txt"
  cp "$data/small-delete-me.txt" "$data/child/nested/small-remove-me.txt"
  printf 'finder metadata\n' > "$data/metadata/.DS_Store"
  printf 'existing destination\n' > "$data/child/existing.txt"
  printf 'nested content with the same filename\n' > "$data/child/nested/existing.txt"
  ln -s /etc/passwd "$data/child/nested/delete-me-link"
  cp -a "$data/." "$expected/"

  case "$mode" in
    dedup)
      mv "$expected/a-original.txt" "$expected/longer-duplicate-name.txt"
      rm "$expected/child/nested/longer-duplicate-name.txt" "$expected/metadata/.DS_Store"
      rmdir "$expected/empty/nested" "$expected/empty" "$expected/metadata"
      ;;
    delete-files-including)
      rm "$expected/delete-me.tmp" "$expected/remove-me.tmp"
      ;;
    flatten)
      mv "$expected/child/nested/longer-duplicate-name.txt" "$expected/child/longer-duplicate-name.txt"
      mv "$expected/child/nested/existing.txt" "$expected/child/existing (1).txt"
      rmdir "$expected/empty/nested"
      ;;
  esac

  # DRY_RUN defaults to true. A read-only mount also catches attempted writes.
  before="$(snapshot "$data")"
  dry_run="$(run_cleanup ',readonly' 2>&1)"
  printf '%s\n' "$dry_run"
  grep -Fq 'Would ' <<< "$dry_run"
  test "$before" = "$(snapshot "$data")"

  # Applying changes to a read-only volume must fail without changing data.
  if run_cleanup ',readonly' -e DRY_RUN=false; then
    echo 'Cleanup unexpectedly succeeded on a read-only data mount.' >&2
    exit 1
  fi
  test "$before" = "$(snapshot "$data")"

  run_cleanup '' -e DRY_RUN=false
  snapshot "$expected" > "$fixture/expected.snapshot"
  snapshot "$data" > "$fixture/actual.snapshot"
  diff -u "$fixture/expected.snapshot" "$fixture/actual.snapshot"

  # Invalid modes must fail before making any edits, even with DRY_RUN=false.
  before="$(snapshot "$data")"
  if run_cleanup ',readonly' -e MODE=invalid -e DRY_RUN=false; then
    echo 'Invalid mode unexpectedly succeeded.' >&2
    exit 1
  fi
  test "$before" = "$(snapshot "$data")"
done
done

# Exercise a real cross-filesystem copy as a non-root container user. Its
# nested tmpfs must remain mounted even after all eligible files move out.
data="$fixture/cross-filesystem"
mkdir -p "$data/child/mounted"
docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges \
  --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$data,dst=/dedup" \
  --tmpfs "/dedup/child/mounted:rw,uid=$(id -u),gid=$(id -g),mode=0700" \
  -e MODE=flatten -e DRY_RUN=false --entrypoint /bin/sh "$LOCAL_IMAGE" -ec '
    printf "cross-filesystem data\n" > /dedup/child/mounted/file.txt
    chmod 0640 /dedup/child/mounted/file.txt
    original="$(stat -c "%u:%g:%a:%Y" /dedup/child/mounted/file.txt)"
    /app/main
    test "$(cat /dedup/child/file.txt)" = "cross-filesystem data"
    test "$(stat -c "%u:%g:%a:%Y" /dedup/child/file.txt)" = "$original"
    test -d /dedup/child/mounted
    test -z "$(ls -A /dedup/child/mounted)"
  '

# A bind-mounted alias of a subtree must not be processed as a second root.
data="$fixture/bind-alias"
mkdir -p "$data/sub"
printf 'preserve alias data\n' > "$data/sub/delete-me"
before="$(snapshot "$data")"
if docker run --rm --network none --read-only \
  --mount "type=bind,src=$data,dst=/dedup" \
  --mount "type=bind,src=$data/sub,dst=/alias" \
  -e ROOT=/dedup,/alias -e MODE=delete-files-including \
  -e DELETE_FILES_INCLUDING=delete-me -e DRY_RUN=false "$LOCAL_IMAGE"; then
  echo 'Cleanup accepted overlapping bind-mounted roots.' >&2
  exit 1
fi
test "$before" = "$(snapshot "$data")"

echo 'Root and non-root modes, dry runs, read-only volumes, symlinks, filters, collisions, cross-filesystem copies and bind aliases passed.'
