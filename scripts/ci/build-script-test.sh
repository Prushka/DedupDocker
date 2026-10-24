#!/usr/bin/env bash
set -euo pipefail

repository="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf -- "$fixture"' EXIT
mkdir "$fixture/bin"

# Intercept both commands: these checks never contact Docker or publish an image.
cat > "$fixture/bin/git" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
[[ "$*" == 'describe --tags --dirty --always' ]]
if [[ "${GIT_TEST_STATUS:-0}" != 0 ]]; then
  exit "$GIT_TEST_STATUS"
fi
printf '%s\n' 'audit version'
STUB

cat > "$fixture/bin/docker" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'called\n' >> "$DOCKER_TEST_LOG"
[[ $# == 12 && "$1" == buildx && "$2" == build && "$3" == --platform &&
  "$4" == linux/amd64 && "$5" == -f && "$6" == Dockerfile && "$7" == --tag &&
  "$8" == 'meinya/dedup:audit version' && "$9" == --tag &&
  "${10}" == meinya/dedup:latest && "${11}" == --push && "${12}" == . ]] || exit 98
exit "${DOCKER_TEST_STATUS:-0}"
STUB
chmod +x "$fixture/bin/git" "$fixture/bin/docker"
export PATH="$fixture/bin:$PATH"
export DOCKER_TEST_LOG="$fixture/docker.log"

status=0
GIT_TEST_STATUS=0 DOCKER_TEST_STATUS=7 bash "$repository/build.sh" > "$fixture/output" 2>&1 || status=$?
[[ "$status" == 7 && ! -s "$fixture/output" ]]
[[ "$(wc -l < "$DOCKER_TEST_LOG")" == 1 ]]

# A failed version lookup must stop before the build/publish command is called.
: > "$DOCKER_TEST_LOG"
status=0
GIT_TEST_STATUS=9 DOCKER_TEST_STATUS=0 bash "$repository/build.sh" > "$fixture/output" 2>&1 || status=$?
[[ "$status" == 9 && ! -s "$DOCKER_TEST_LOG" && ! -s "$fixture/output" ]]

GIT_TEST_STATUS=0 DOCKER_TEST_STATUS=0 bash "$repository/build.sh" > "$fixture/output" 2>&1
[[ "$(cat "$fixture/output")" == 'audit version' ]]
[[ "$(wc -l < "$DOCKER_TEST_LOG")" == 1 ]]
printf '%s\n' 'Build helper propagates failures and keeps version arguments intact.'
