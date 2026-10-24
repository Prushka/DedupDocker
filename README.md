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
.github/workflows/ GitHub Actions checks and Docker Hub publishing
scripts/ci/       Container smoke test
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

## GitHub Actions

[CI/CD](https://github.com/Prushka/DedupDocker/actions/workflows/docker.yml) runs
on every branch push, pull request, and manual dispatch. It checks the Go code,
validates the workflow and shell script, builds the `linux/amd64` container,
and tests dry-run and cleanup behavior against disposable sample files.

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
