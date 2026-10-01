#!/usr/bin/env bash
set -euo pipefail
name="storeshoppers-check-${RANDOM}"
volume="${name}-data"
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker volume create "$volume" >/dev/null
docker run -d --name "$name" -p 127.0.0.1:18090:8090 \
  -e APP_ORIGIN=http://127.0.0.1:18090 \
  -v "$volume:/data" storeshoppers:test >/dev/null
for attempt in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:18090/healthz >/dev/null; then break; fi
  sleep 1
done
curl -fsS http://127.0.0.1:18090/ -o /tmp/storeshoppers-home.html
grep -q 'Good things.' /tmp/storeshoppers-home.html
curl -fsS http://127.0.0.1:18090/manager/login -o /tmp/storeshoppers-login.html
grep -q 'Manager access is disabled' /tmp/storeshoppers-login.html
test "$(docker exec "$name" id -u)" = 10001
docker exec "$name" /usr/local/bin/shop healthcheck
docker exec "$name" sh -c 'test -s /data/shop.db && echo persistent > /data/persistence-probe'
docker rm -f "$name" >/dev/null
docker run -d --name "$name" -e APP_ORIGIN=http://127.0.0.1:18090 \
  -v "$volume:/data" storeshoppers:test >/dev/null
for attempt in $(seq 1 30); do
  if docker exec "$name" /usr/local/bin/shop healthcheck >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$name" /usr/local/bin/shop healthcheck
test "$(docker exec "$name" cat /data/persistence-probe)" = persistent
docker exec "$name" test -s /data/shop.db
# A public HTTP origin must fail closed if management is configured.
if docker run --rm -e APP_ORIGIN=http://market.example \
  -e MANAGER_PASSWORD=container-test-only-long-password storeshoppers:test; then
  echo 'ERROR: public HTTP manager configuration was accepted' >&2
  exit 1
fi
echo 'PASS: non-root container, health probe, persistent volume, disabled management and HTTP manager refusal'
