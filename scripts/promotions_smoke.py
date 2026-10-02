#!/usr/bin/env python3
"""Real HTTP sale/featured/quote/restart checks on an isolated disposable demo."""
from http.cookiejar import CookieJar
from pathlib import Path
import os
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request
from smoke import Fields, Forms
from order_overrides_smoke import Page

ROOT = Path(__file__).resolve().parent.parent


def main():
    with tempfile.TemporaryDirectory(prefix="shop-promotions-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        db_path = directory + "/shop.db"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=db_path, MANAGER_PASSWORD="password", DEMO_MODE="true")
        client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

        def request(path, fields=None):
            data = None if fields is None else urllib.parse.urlencode(fields).encode()
            with client.open(urllib.request.Request(origin + path, data=data,
                    headers={"Origin": origin}), timeout=5) as response:
                body = response.read().decode()
                Page(body)  # Unique IDs, labels/ARIA targets and non-nested forms
                return response.geturl().removeprefix(origin), body

        def start():
            process = subprocess.Popen([str(ROOT / "bin/shop")], env=env,
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            for _ in range(100):
                try:
                    assert request("/healthz")[1] == "ok\n"
                    return process
                except OSError:
                    if process.poll() is not None:
                        raise AssertionError("promotions server exited")
                    time.sleep(.05)
            process.terminate(); process.wait(timeout=5)
            raise AssertionError("promotions server not ready")

        process = start()
        try:
            _, home = request("/")
            assert "No active sales right now" in home
            csrf = Fields(home).fields["csrf"]
            _, manager = request("/manager/login", {"csrf": csrf, "password": "password"})
            csrf = Fields(manager).fields["csrf"]
            _, preview = request("/manager/promotions/examples")
            examples = Fields(preview).fields
            create = {key: examples[key] for key in ("csrf", "quote", "command_key")}
            with sqlite3.connect(db_path) as db:
                assert db.execute("SELECT count(*) FROM promotions").fetchone()[0] == 0
                stock = db.execute("SELECT id,stock FROM products ORDER BY id").fetchall()
            _, invalid = request("/manager/promotions/examples", create)
            assert "Please check" in invalid
            create["confirm"] = "1"
            request("/manager/promotions/examples", create)
            request("/manager/promotions/examples", create)
            with sqlite3.connect(db_path) as db:
                assert db.execute("SELECT count(*) FROM promotions").fetchone()[0] == 3
                assert db.execute("SELECT count(*) FROM product_features WHERE featured=1").fetchone()[0] == 3
                assert db.execute("SELECT id,stock FROM products ORDER BY id").fetchall() == stock
            _, home = request("/?sales=1&featured=1")
            assert "WEEKLY SALES" in home and "$2.49" in home and "$3.49" in home
            assert "Shop all sales" in home and "featured picks" in home
            cart_fields = dict(csrf=csrf, revision=Fields(home).fields["revision"],
                product_id="1", quantity="2", mode="add", **{"return": "cart"})
            _, basket = request("/cart", cart_fields)
            assert "$4.98" in basket
            fields = Fields(basket).fields
            checkout = {key: fields[key] for key in ("csrf", "revision", "checkout_key", "quote")}
            receipt_path, receipt = request("/checkout", checkout)
            assert "$4.98" in receipt
            _, home = request("/")
            cart_fields["revision"] = Fields(home).fields["revision"]
            _, basket = request("/cart", cart_fields)
            fields = Fields(basket).fields
            stale_quote = {key: fields[key] for key in ("csrf", "revision", "checkout_key", "quote")}
            _, edit = request("/manager/promotions/1")
            assert 'name="sale_price_usd"' in edit and "UTC" in edit
            cancel = next(f for f in Forms(edit).forms if f["action"].endswith("/1/cancel"))
            request(cancel["action"], cancel["fields"])
            _, stale = request("/checkout", stale_quote)
            assert "Review the current basket" in stale and "$6.98" in stale
            fields = Fields(stale).fields
            request("/checkout", {key: fields[key] for key in ("csrf", "revision", "checkout_key", "quote")})
            assert "$4.98" in request(receipt_path)[1]
            _, featured = request("/manager/featured?edit=1")
            feature_form = next(f for f in Forms(featured).forms if f["action"] == "/manager/featured/1")
            values = dict(feature_form["fields"]); values.pop("featured", None)
            request(feature_form["action"], values)
            process.terminate(); process.wait(timeout=5)
            process = start()
            assert "$4.98" in request(receipt_path)[1]
            with sqlite3.connect(db_path) as db:
                assert db.execute("SELECT cancelled FROM promotions WHERE id=1").fetchone()[0] == 1
                assert db.execute("SELECT featured FROM product_features WHERE product_id=1").fetchone()[0] == 0
                assert db.execute("SELECT price FROM order_items ORDER BY order_id").fetchall() == [(249,), (349,)]
                assert db.execute("SELECT MAX(version) FROM schema_version").fetchone()[0] == 14
            assert not list(Path(db_path + ".demo-backups").glob("*.sqlite3"))
            print("Promotion form smoke passed: empty state, confirmed/replay-safe examples, active/regular rates, featured filter, stock-only holds, stale quote review, immutable receipts, cancel/unfeature and restart persistence")
        finally:
            process.terminate(); process.wait(timeout=5)


if __name__ == "__main__":
    main()
