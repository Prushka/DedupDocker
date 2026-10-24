# DedupDocker

A file cleanup utility with duplicate removal, filename matching, and folder
flattening modes. `DRY_RUN` defaults to `true`: the app previews operations
without creating, moving, renaming, or deleting files or directories. Set
`DRY_RUN=false` to apply the selected mode.

## Project layout

```text
cmd/dedup/main.go  Command entry point
internal/app/     Configuration and file cleanup implementation
Dockerfile        Container build
build.sh          Image build and publish script
.github/workflows/ GitHub Actions checks and Docker Hub publishing
scripts/ci/       Container smoke test
```

## Build and run

Requires Go 1.27.3 or later. From the repository root:

```sh
go build -o dedup ./cmd/dedup
ROOT=/path/to/files go run ./cmd/dedup
```

In PowerShell, set the root directory before running:

```powershell
$env:ROOT = 'C:\path\to\files'
go run ./cmd/dedup
```

Configuration uses environment variables. `ROOT` defaults to `/dedup`, and
`MODE` defaults to `dedup`.

## Modes and configuration

| Variable | Default | Behavior |
| --- | --- | --- |
| `MODE` | `dedup` | `dedup`, `delete-files-including`, or `flatten` |
| `ROOT` | `/dedup` | Comma-separated directories, processed independently; physically overlapping roots, including ancestor aliases, are rejected |
| `DRY_RUN` | `true` | Preview operations; set `false` to apply changes |
| `EMPTY_DIR` | `false` | In dedup mode, also remove empty directories |
| `DELETE_FILES_INCLUDING` | empty | Comma-separated, case-sensitive filename substrings for delete-files-including mode |
| `MIN_SIZE` | `0` | Minimum eligible file size in bytes, inclusive, in every mode |
| `EXCLUDE_EXT` | empty | Comma-separated extensions including the dot, matched without regard to case, in every mode |
| `IGNORE_KEYWORDS` | empty | Comma-separated substrings matched against full paths without regard to case; matching files and directory subtrees stay untouched |
| `LOG` | `debug` | `trace`, `debug`, `info`, `warn`, `error`, `fatal`, or `panic` |

Empty list entries are ignored. Invalid configuration or unreadable directories
cause an error before planned edits begin. `DO_REMOVE` and `DEDUP` have been
removed; replace them with `DRY_RUN` and `MODE` in existing configurations.

`MODE=dedup` compares eligible files by size and SHA-256, then confirms their
bytes before removing copies. It keeps a copy in the shortest directory path,
using the longest duplicate filename where available. Ties are resolved
lexically. An unrelated existing destination is preserved and the retained
copy keeps its original name. With `EMPTY_DIR=true`, it also removes empty
directories and eligible `.DS_Store` files; excluded files keep their folders.
Metadata and ordinary files are deduplicated separately, so `.DS_Store` can
never become an ordinary file's surviving name. It does not run filename-matching
deletion. Files with additional NTFS streams or Linux extended attributes are
preserved during dedup and metadata cleanup rather than losing their extra data.
Names that are already hardlinks to the retained file are preserved because
they already share its storage.

`MODE=delete-files-including` only deletes eligible files whose filenames
contain an entry from `DELETE_FILES_INCLUDING`. An empty list does nothing.
It does not deduplicate or remove empty directories, even with `EMPTY_DIR=true`.

`MODE=flatten` moves eligible files from nested directories up into their
containing direct child of `ROOT`. Files directly in `ROOT` and files already
directly inside a child stay in place. Each child is handled separately.
Collisions keep both files using names such as `photo (1).jpg`, incrementing
the suffix as needed. Identical files are preserved too. Newly empty nested
directories are removed regardless of `EMPTY_DIR`, while `ROOT` and its direct
children stay. Excluded files remain at their original paths, so their parent
folders remain when necessary. `.DS_Store` is an ordinary file in this mode.
Long collision names are shortened without splitting UTF-8 characters to keep
the numbered filename within a 255-byte component limit. Extensions are kept
intact when they fit. Windows destination names are reserved without regard to
case; distinct source files in case-sensitive directories are all processed.

For example, flatten turns `ROOT/album/disc1/photo.jpg` into
`ROOT/album/photo.jpg`, keeping `album` separate from other child folders.

Moves never overwrite an existing path. The app skips symbolic links, checks
source identities and modification times, and rechecks duplicate contents
before deletion. A detected change or I/O failure stops processing and returns
a nonzero exit status. Earlier completed edits remain; operations are not a
transaction. Native moves preserve file attributes and additional streams.
Copy-based moves preserve file permissions and modification time and verify the
copied contents before removing the source. If a copy cannot preserve additional
streams or extended attributes, it fails with the source intact.
The guarded file operations support Linux and Windows. Other platforms return
an unsupported-operation error instead of using unguarded destructive operations.

## Check and build the container

```sh
go test ./...
go vet ./...
docker build -t dedup .
```

## GitHub Actions

[CI/CD](https://github.com/Prushka/DedupDocker/actions/workflows/docker.yml) runs
on every branch push, pull request, and manual dispatch. It checks the Go code on
Linux and Windows, validates the workflow and shell scripts, verifies that build
helper failures are reported, builds the `linux/amd64` container,
and tests all three modes, dry-run behavior, shared filters, and collisions
against disposable sample files.

After the checks pass, pushes and manual runs publish the tested image to
`meinya/dedup:<7-character-commit>`. The current default-branch commit also
updates `meinya/dedup:latest`; promotions are serialized and skip older commits
when the default branch has advanced. Pull requests build and test without
logging in to Docker Hub or publishing.

Publishing uses repository Actions secrets `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN`. The token needs write access to `meinya/dedup`.

To run the container smoke test locally:

```sh
docker build -t dedup-ci .
LOCAL_IMAGE=dedup-ci bash scripts/ci/docker-smoke.sh
```
