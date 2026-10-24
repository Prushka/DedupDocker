#!/usr/bin/env bash
set -euo pipefail

: "${LOCAL_IMAGE:?Set LOCAL_IMAGE to the built Docker image}"

fixture="$(mktemp -d)"
trap 'rm -rf -- "$fixture"' EXIT
mkdir -p "$fixture/data/nested" "$fixture/data/empty/nested" "$fixture/data/metadata"
mkdir -p "$fixture/expected/nested"

printf 'duplicate content\n' > "$fixture/data/a-original.txt"
cp "$fixture/data/a-original.txt" "$fixture/data/nested/longer-duplicate-name.txt"
printf 'unique content to preserve\n' > "$fixture/data/keep.txt"
printf 'delete by filename\n' > "$fixture/data/delete-me.tmp"
printf 'keep because of ignored keyword\n' > "$fixture/data/protected-delete-me.tmp"
printf 'excluded duplicate content\n' > "$fixture/data/first.skip"
cp "$fixture/data/first.skip" "$fixture/data/nested/second.skip"
printf 'x' > "$fixture/data/small-a.txt"
cp "$fixture/data/small-a.txt" "$fixture/data/small-b.txt"
printf 'finder metadata\n' > "$fixture/data/metadata/.DS_Store"

cp "$fixture/data/a-original.txt" "$fixture/expected/longer-duplicate-name.txt"
for file in keep.txt protected-delete-me.tmp first.skip small-a.txt small-b.txt; do
  cp "$fixture/data/$file" "$fixture/expected/$file"
done
cp "$fixture/data/nested/second.skip" "$fixture/expected/nested/second.skip"

snapshot() {
  (
    cd "$1"
    find . -printf '%P %y\n' | sort
    find . -type f -print0 | sort -z | xargs -0 sha256sum
  )
}

run_cleanup() {
  docker run --rm --network none --read-only \
    --mount "type=bind,src=$fixture/data,dst=/dedup$1" \
    -e LOG=debug -e EMPTY_DIR=true -e MIN_SIZE=4 -e EXCLUDE_EXT=.skip \
    -e DELETE_FILES_INCLUDING=delete-me -e IGNORE_KEYWORDS=protected \
    "${@:2}" "$LOCAL_IMAGE"
}

# Exercise the default dry run with a read-only fixture and verify every file.
before="$(snapshot "$fixture/data")"
dry_run="$(run_cleanup ',readonly' 2>&1)"
printf '%s\n' "$dry_run"
grep -Fq 'Would delete:' <<< "$dry_run"
grep -Fq 'Would rename:' <<< "$dry_run"
test "$before" = "$(snapshot "$fixture/data")"

# Apply changes only to this disposable fixture, then compare names and contents.
run_cleanup '' -e DO_REMOVE=true
snapshot "$fixture/expected" > "$fixture/expected.snapshot"
snapshot "$fixture/data" > "$fixture/actual.snapshot"
diff -u "$fixture/expected.snapshot" "$fixture/actual.snapshot"

echo 'Container passed dry-run, duplicate removal and rename, filename deletion, empty directory, ignored keyword, extension and minimum-size checks.'
