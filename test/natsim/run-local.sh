#!/usr/bin/env bash
# Runs the NAT simulation on a developer machine (Git Bash or Linux) or in CI.
# Needs Go, and Docker with a Linux engine that allows privileged containers.
#
#   test/natsim/run-local.sh                  # all scenarios
#   SCENARIOS="easy-easy hard-hard" test/natsim/run-local.sh
#
# Output: test/natsim/out/summary.md and one directory of logs per scenario.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
name="lanfolk-natsim-$$"
image="lanfolk-natsim:local"

# Linux binaries from the host. The service uses wireguard-go and needs no cgo.
cd "$root"
mkdir -p "$here/bin"
for cmd in lanfolk-relay lanfolk-service lanfolk-cli; do
  echo "build $cmd (linux/${GOARCH:-amd64})"
  CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH:-amd64}" \
    go build -trimpath -o "$here/bin/$cmd" "./cmd/$cmd"
done

# MSYS_NO_PATHCONV stops Git Bash from rewriting Linux paths such as /out.
# All docker commands run in test/natsim so that no host path is passed.
export MSYS_NO_PATHCONV=1
cd "$here"
docker build -t "$image" .

rm -rf out
mkdir -p out
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT

env_args=()
for v in SCENARIOS SETTLE_MAX; do
  if [ -n "${!v:-}" ]; then env_args+=(-e "$v=${!v}"); fi
done

# On a Linux host, expose its kernel modules so that the container can load
# sch_netem. Docker Desktop's kernel has none; the impairment scenario then
# falls back to nftables loss (see README).
mod_args=()
if [ "$(uname -s)" = Linux ] && [ -d /lib/modules ]; then
  mod_args=(-v /lib/modules:/lib/modules:ro)
fi

docker create --name "$name" --privileged --device /dev/net/tun \
  --tmpfs /run --tmpfs /tmp "${mod_args[@]}" "${env_args[@]}" "$image" >/dev/null

rc=0
docker start -a "$name" || rc=$?

# Copy the results out even when scenarios failed.
docker cp "$name:/out/." out || echo "warning: could not copy /out from the container" >&2

if [ -f out/summary.md ]; then
  echo
  echo "Summary written to test/natsim/out/summary.md"
fi
exit "$rc"
