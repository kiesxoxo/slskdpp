#!/usr/bin/env bash
# Runs an image and checks the web UI and API answer, that Docker finds
# it healthy through the image's own HEALTHCHECK, and with a version
# argument that the binary reports it. A closed port stands in for the
# Soulseek server: the client keeps retrying, the web UI and API still
# answer, and nothing reaches the real network.
#
# Usage: smoke-test.sh IMAGE [VERSION]
set -euo pipefail

image=$1
version=${2:-}
name=slskdpp-smoke-$$

trap 'docker logs "$name" || true; docker rm -f "$name" >/dev/null 2>&1 || true' EXIT

# A short health interval keeps the test quick; the check itself is the
# image's.
docker run -d --name "$name" -p 5030:5030 -e SLSKDPP_SERVER=127.0.0.1:9 --health-interval=2s \
  -e SLSKDPP_USERNAME=ci -e SLSKDPP_PASSWORD=ci -e SLSKDPP_API_KEY=ci-key "$image"
for _ in $(seq 30); do curl -sf -o /dev/null localhost:5030/ && break; sleep 1; done
curl -sf localhost:5030/ | grep -q '<div id="app">'
test "$(curl -s -o /dev/null -w '%{http_code}' localhost:5030/api/v1/server)" = 401
curl -sf -H 'X-API-Key: ci-key' localhost:5030/api/v0/application/version
echo
# Not connected to the closed port, so Degraded, which is still healthy.
test "$(curl -sf localhost:5030/health)" = Degraded
for _ in $(seq 30); do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$name")
  [ "$status" = healthy ] && break
  sleep 1
done
echo "health: $status"
test "$status" = healthy

if [ -n "$version" ]; then
  docker run --rm --entrypoint /usr/local/bin/slskdpp "$image" -version | tee /dev/stderr | grep -qF "$version"
fi

docker image inspect "$image" --format 'image size: {{.Size}}'
