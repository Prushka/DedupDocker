# DedupDocker

A file cleanup utility for finding duplicates, deleting files by name, and
removing empty directories. Operations run in dry-run mode by default; set
`DO_REMOVE=true` to apply changes.

## Project layout

```text
cmd/dedup/main.go  Command entry point
internal/app/     Configuration and file cleanup implementation
Dockerfile        Container build
build.sh          Image build and publish script
```

## Build and run

From the repository root:

```sh
go build -o dedup ./cmd/dedup
ROOT=/path/to/files go run ./cmd/dedup
```

In PowerShell, set the root directory before running:

```powershell
$env:ROOT = 'C:\path\to\files'
go run ./cmd/dedup
```

Configuration uses environment variables defined in `internal/app/config.go`.
`ROOT` defaults to `/dedup`, and duplicate detection is enabled by default.

## Check and build the container

```sh
go test ./...
go vet ./...
docker build -t dedup .
```
