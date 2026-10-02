#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
probe_dir="$(mktemp -d -t storeshoppers-container-demo.XXXXXX)"
name="storeshoppers-check-${RANDOM}"
volume="${name}-data"
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  rm -rf -- "$probe_dir"
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
grep -q 'No active sales right now' /tmp/storeshoppers-home.html
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
# The explicit shared demo flag may use a simple password, but never over public HTTP.
if docker run --rm -e APP_ORIGIN=http://market.example -e DEMO_MODE=true \
  -e MANAGER_PASSWORD=password storeshoppers:test; then
  echo 'ERROR: demo mode bypassed public HTTPS requirement' >&2
  exit 1
fi
docker rm -f "$name" >/dev/null
docker run -d --name "$name" -e APP_ORIGIN=https://market.example \
  -e DEMO_MODE=true -e MANAGER_PASSWORD=password -v "$volume:/data" storeshoppers:test >/dev/null
for attempt in $(seq 1 30); do
  if docker exec "$name" /usr/local/bin/shop healthcheck >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$name" /usr/local/bin/shop healthcheck
# Verify ordinary demo restart persistence, then explicitly reset the demo. The
# HTTPS policy above remains separate from this loopback-only HTTP fixture.
docker rm -f "$name" >/dev/null
docker run -d --name "$name" -p 127.0.0.1:18090:8090 \
  -e APP_ORIGIN=http://127.0.0.1:18090 -e DEMO_MODE=true -e MANAGER_PASSWORD=password \
  -v "$volume:/data" storeshoppers:test >/dev/null
for attempt in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:18090/healthz >/dev/null; then break; fi
  sleep 1
done
docker exec "$name" /usr/local/bin/shop healthcheck
demo_probe() {
  python3 - "$probe_dir" "$1" <<'PYTHON'
from http.cookiejar import MozillaCookieJar
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request
sys.path.insert(0, "scripts")
from smoke import Fields
root, phase = Path(sys.argv[1]), sys.argv[2]
origin = "http://127.0.0.1:18090"
jar = MozillaCookieJar(str(root / "cookies.txt"))
if phase != "before":
    jar.load(ignore_discard=True)
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
def get(path):
    with client.open(origin + path, timeout=5) as response:
        return response.read().decode()
def post(path, fields):
    request = urllib.request.Request(origin + path, data=urllib.parse.urlencode(fields).encode(),
                                     headers={"Origin": origin})
    with client.open(request, timeout=5) as response:
        return response.geturl().removeprefix(origin), response.read().decode()
if phase == "before":
    fields = Fields(get("/")).fields
    _, basket = post("/cart", {"csrf":fields["csrf"],"revision":fields["revision"],
        "product_id":1,"quantity":2,"mode":"add","return":"cart"})
    fields = Fields(basket).fields
    receipt_path, receipt = post("/checkout", {key:fields[key] for key in
        ("csrf","revision","checkout_key","quote")})
    assert "DEMO-" in receipt
    (root / "receipt.txt").write_text(receipt_path)
    jar.save(ignore_discard=True)
else:
    assert "DEMO-" in get((root / "receipt.txt").read_text()), "Ordinary restart lost order"
    fields = Fields(get("/manager/login")).fields
    post("/manager/login", {"csrf":fields["csrf"],"password":"password"})
    confirmation = get("/manager/demo/reset")
    assert "This affects every visitor" in confirmation
    assert "DEMO-" in get((root / "receipt.txt").read_text()), "GET reset changed data"
    _, complete = post("/manager/demo/reset", {"csrf":Fields(confirmation).fields["csrf"],
                                              "confirm":"reset-shared-demo"})
    assert "The shared demo is back to its defaults." in complete
    assert "Honeycrisp apples" in get("/")
    try:
        get((root / "receipt.txt").read_text())
        raise AssertionError("Demo receipt survived confirmed reset")
    except urllib.error.HTTPError as error:
        assert error.code == 404
PYTHON
}
demo_probe before
docker stop "$name" >/dev/null
docker start "$name" >/dev/null
for attempt in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:18090/healthz >/dev/null; then break; fi
  sleep 1
done
docker exec "$name" /usr/local/bin/shop healthcheck
demo_probe after
# Stop before copying the live database; the reset archive itself is standalone.
docker stop "$name" >/dev/null
docker cp "$name:/data/shop.db" "$probe_dir/reset.db"
docker cp "$name:/data/shop.db.demo-backups" "$probe_dir/backups"
python3 - "$probe_dir" <<'PYTHON'
from contextlib import closing
from pathlib import Path
import sqlite3
import sys
root = Path(sys.argv[1])
with closing(sqlite3.connect(root / "reset.db")) as db:
    assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    assert db.execute("SELECT COUNT(*) FROM products").fetchone()[0] == 70
    assert db.execute("SELECT COUNT(*) FROM orders").fetchone()[0] == 0
    assert db.execute("SELECT stock FROM products WHERE id=1").fetchone()[0] == 24
preserved_order = False
for path in (root / "backups").glob("*.sqlite3"):
    with closing(sqlite3.connect(f"file:{path}?mode=ro", uri=True)) as db:
        assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
        preserved_order |= db.execute("SELECT COUNT(*) FROM orders").fetchone()[0] == 1
assert preserved_order, "No readable pre-reset backup retained the order"
PYTHON
echo 'PASS: non-root container, health probe, normal persistent volume, management defaults, HTTPS-only demo gate, demo restart persistence, protected explicit reset and readable preserved backup'
