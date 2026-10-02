#!/usr/bin/env python3
"""Exercise explicit DEMO_MODE manager reset through real HTTP in a disposable DB."""
from contextlib import closing
from http.cookiejar import CookieJar
from pathlib import Path
import os
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from smoke import Fields, Forms

ROOT = Path(__file__).resolve().parent.parent


def main():
    with tempfile.TemporaryDirectory(prefix="shopper-demo-reset-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        path = Path(directory) / "shop.db"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=str(path), MANAGER_PASSWORD="password", DEMO_MODE="true",
                   DEMO_BACKUP_MAX_BYTES=str(256 << 20))
        jar = CookieJar()
        client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

        def get(route):
            with client.open(origin + route, timeout=5) as response:
                return response.read().decode()

        def post(route, fields):
            request = urllib.request.Request(origin + route,
                data=urllib.parse.urlencode(fields).encode(), headers={"Origin": origin})
            with client.open(request, timeout=5) as response:
                return response.geturl().removeprefix(origin), response.read().decode()

        def start():
            process = subprocess.Popen([str(ROOT / "bin/shop")], env=env,
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError("Server exited during startup: " + process.stderr.read())
                try:
                    assert get("/healthz") == "ok\n"
                    subprocess.run([str(ROOT / "bin/shop"), "healthcheck"], env=env,
                                   check=True, capture_output=True, timeout=5)
                    return process
                except (OSError, AssertionError):
                    time.sleep(.05)
            process.terminate()
            process.wait(timeout=5)
            raise RuntimeError("Server did not become ready: " + process.stderr.read())

        def stop(process):
            process.terminate()
            process.wait(timeout=5)
            process.stderr.close()

        process = start()
        try:
            # Even demo-mode health-only restarts must not create reset archives.
            stop(process)
            process = start()
            assert not list(Path(str(path) + ".demo-backups").glob("*.sqlite3"))
            home = get("/")
            csrf = Fields(home).fields["csrf"]
            _, basket = post("/cart", {"csrf":csrf,"revision":Fields(home).fields["revision"],
                "product_id":1,"quantity":2,"mode":"add","return":"cart"})
            fields = Fields(basket).fields
            receipt_path, receipt = post("/checkout", {key:fields[key] for key in
                ("csrf","revision","checkout_key","quote")})
            assert "$6.98" in receipt and "DEMO-" in receipt
            home = get("/")
            post("/cart", {"csrf":csrf,"revision":Fields(home).fields["revision"],
                "product_id":2,"quantity":3,"mode":"add","return":"cart"})
            _, manager = post("/manager/login", {"csrf":csrf,"password":"password"})
            csrf = Fields(manager).fields["csrf"]
            stock = get("/manager/stock?product=1")
            inventory = Forms(stock).named("product_id", "1", "/manager/inventory")
            post(inventory["action"], dict(inventory["fields"], delta="4", reason="Demo reset HTTP audit"))
            _, labels = post("/manager/catalog/categories", {"csrf":csrf,"name":"Reset test department"})
            category = Forms(labels).named("name", "Reset test department", "/manager/catalog/categories/")
            category_id = category["action"].split("/")[-1]
            _, catalog = post("/manager/catalog/products", {"csrf":csrf,"sku":"RESET-HTTP-ITEM",
                "name":"Reset test product","description":"Temporary fake fixture","category_id":category_id,
                "type_id":0,"icon":"bread","price":999,"sale_unit":"each","quantity_step":1})
            assert "Reset test product" in catalog
            post("/manager/baskets/practice", {"csrf":csrf})
            # Public routes cannot fetch archives or perform a reset.
            for route in ("/reset", "/demo/reset", "/shop.db.demo-backups/"):
                try:
                    get(route)
                    raise AssertionError("Unexpected public reset/archive route: " + route)
                except urllib.error.HTTPError as error:
                    assert error.code == 404
            stop(process)
            with closing(sqlite3.connect(path)) as db:
                assert db.execute("SELECT COUNT(*) FROM products").fetchone()[0] == 73
                assert db.execute("SELECT COUNT(*) FROM orders").fetchone()[0] == 1
                assert db.execute("SELECT COUNT(*) FROM baskets WHERE synthetic=1").fetchone()[0] == 2
            # Bad app config must fail before it resets anything.
            invalid = subprocess.run([str(ROOT / "bin/shop")], env=dict(env, APP_ORIGIN="bad-origin"),
                                     capture_output=True, text=True, timeout=5)
            assert invalid.returncode != 0 and "APP_ORIGIN" in invalid.stderr
            with closing(sqlite3.connect(path)) as db:
                assert db.execute("SELECT COUNT(*) FROM products").fetchone()[0] == 73
            process = start()
            # Ordinary demo restarts persist all data and grants. Only the
            # manager's explicitly confirmed POST resets the shared demo.
            assert "DEMO-" in get(receipt_path)
            assert "Reset test product" in get("/")
            confirmation = get("/manager/demo/reset")
            assert "This affects every visitor" in confirmation
            assert "Reset test product" in get("/")
            reset_csrf = Fields(confirmation).fields["csrf"]
            try:
                post("/manager/demo/reset", {"csrf":reset_csrf})
                raise AssertionError("Reset accepted without explicit confirmation")
            except urllib.error.HTTPError as error:
                assert error.code == 400
            _, complete = post("/manager/demo/reset", {"csrf":reset_csrf,"confirm":"reset-shared-demo"})
            assert "The shared demo is back to its defaults." in complete
            assert ".demo-backups" not in complete
            assert "Manager demo access" in get("/manager")
            assert "Reset test product" not in get("/")
            try:
                get(receipt_path)
                raise AssertionError("Old receipt survived explicit demo reset")
            except urllib.error.HTTPError as error:
                assert error.code == 404
            stop(process)
            with closing(sqlite3.connect(path)) as db:
                assert db.execute("SELECT COUNT(*) FROM products").fetchone()[0] == 72
                assert db.execute("SELECT stock FROM products WHERE id=1").fetchone()[0] == 24
                assert db.execute("SELECT COUNT(*) FROM orders").fetchone()[0] == 0
                assert db.execute("SELECT COUNT(*) FROM cart").fetchone()[0] == 0
                assert db.execute("SELECT COUNT(*) FROM baskets WHERE synthetic=1").fetchone()[0] == 0
                assert db.execute("SELECT COUNT(*) FROM adjustments").fetchone()[0] == 0
                assert db.execute("SELECT COUNT(*) FROM categories WHERE name='Reset test department'").fetchone()[0] == 0
            backups = list(Path(str(path) + ".demo-backups").glob("*.sqlite3"))
            assert len(backups) == 1
            with closing(sqlite3.connect(f"file:{backups[0]}?mode=ro", uri=True)) as db:
                assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
                assert db.execute("SELECT COUNT(*) FROM products").fetchone()[0] == 73
                assert db.execute("SELECT COUNT(*) FROM orders").fetchone()[0] == 1
                assert db.execute("SELECT COUNT(*) FROM adjustments").fetchone()[0] == 1
                assert db.execute("SELECT COUNT(*) FROM baskets WHERE synthetic=1").fetchone()[0] == 2
            print("PASS: real HTTP demo mutations, catalog/taxonomy, orders/holds/practice baskets, "
                  "stock audit, ordinary restart persistence, GET confirmation, protected POST reset, private complete backup, "
                  "manager grant/cookie invalidation and explicit confirmation")
        finally:
            if process.poll() is None:
                stop(process)


if __name__ == "__main__":
    main()
