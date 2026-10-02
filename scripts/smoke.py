#!/usr/bin/env python3
"""Run the built server and exercise real HTTP form flows with a disposable DB."""
from html.parser import HTMLParser
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

ROOT = Path(__file__).resolve().parent.parent
class Fields(HTMLParser):
    def __init__(self, text):
        super().__init__()
        self.fields = {}
        self.feed(text)
    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "input" and attrs.get("type") == "hidden":
            self.fields.setdefault(attrs.get("name"), attrs.get("value", ""))

class Forms(HTMLParser):
    """Collect named inputs by form action without relying on layout/order."""
    def __init__(self, text):
        super().__init__()
        self.forms = []
        self.current = None
        self.feed(text)
    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "form":
            self.current = {"action": attrs.get("action", ""), "fields": {}}
        elif tag == "input" and self.current is not None and attrs.get("name"):
            self.current["fields"][attrs["name"]] = attrs.get("value", "")
    def handle_endtag(self, tag):
        if tag == "form" and self.current is not None:
            self.forms.append(self.current)
            self.current = None
    def named(self, field, value, action_prefix):
        for form in self.forms:
            if form["action"].startswith(action_prefix) and form["fields"].get(field) == value:
                return form
        raise AssertionError(f"Form for {field}={value!r} absent")

def main():
    with tempfile.TemporaryDirectory(prefix="shopper-smoke-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=f"{directory}/shop.db", MANAGER_PASSWORD="smoke-demo-password")
        jar = CookieJar()
        client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
        def get(path):
            with client.open(origin + path, timeout=5) as response:
                return response.read().decode()
        def post(path, fields):
            req = urllib.request.Request(origin + path,
                data=urllib.parse.urlencode(fields).encode(), headers={"Origin": origin})
            with client.open(req, timeout=5) as response:
                return response.geturl().removeprefix(origin), response.read().decode()
        def start():
            process = subprocess.Popen([str(ROOT / "bin/shop")], env=env,
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            for _ in range(100):
                try:
                    assert get("/healthz") == "ok\n"
                    subprocess.run([str(ROOT / "bin/shop"), "healthcheck"], env=env, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    return process
                except (OSError, AssertionError):
                    if process.poll() is not None:
                        raise RuntimeError("Server exited during startup")
                    time.sleep(.05)
            process.terminate()
            raise RuntimeError("Server did not become ready")
        process = start()
        try:
            home = get("/")
            assert "No active sales right now" in home and "Honeycrisp apples" in home
            assert "ZgotmplZ" not in home
            assert "htmx" in get("/static/htmx.min.js")
            csrf = Fields(home).fields["csrf"]
            _, basket = post("/cart", {"csrf":csrf,"revision":Fields(home).fields["revision"],"product_id":1,"quantity":2,"mode":"add","return":"cart"})
            assert "$6.98" in basket
            fields = Fields(basket).fields
            checkout_fields = {key:fields[key] for key in ("csrf","revision","checkout_key","quote")}
            checkout_fields["instructions"] = "Keep demo bread apart <script>fake</script>"
            receipt_path, receipt = post("/checkout", checkout_fields)
            assert "Keep demo bread apart &lt;script&gt;fake&lt;/script&gt;" in receipt
            assert "0% shopped" in receipt
            assert "DEMO-" in receipt and "$6.98" in receipt and "Placed" in receipt
            replay_path, _ = post("/checkout", {key:fields[key] for key in ("csrf","revision","checkout_key","quote")})
            assert replay_path == receipt_path
            login = get("/manager/login")
            _, manager = post("/manager/login", {"csrf":Fields(login).fields["csrf"],"password":"smoke-demo-password"})
            assert "Store workspace" in manager and "DEMO-" in manager
            fields = Fields(manager).fields
            _, manager = post("/manager/inventory", {"csrf":fields["csrf"],"product_id":1,"version":2,"delta":3,"reason":"HTTP smoke restock"})
            assert "HTTP smoke restock" in manager
            order_id = receipt_path.split('/')[-1]
            picking_path = f"/manager/orders/{order_id}"
            _, manager = post(picking_path + "/advance", {"csrf":fields["csrf"],"status":"Placed","order_version":Fields(get(picking_path)).fields["order_version"]})
            assert "Picking" in get(receipt_path)
            # Readiness is blocked until the required quantity has been picked.
            _, blocked = post(picking_path + "/advance", {"csrf":fields["csrf"],"status":"Picking","order_version":Fields(get(picking_path)).fields["order_version"]})
            assert "Pick every required item" in blocked
            _, manager = post(picking_path + "/items/1", {"csrf":fields["csrf"],"picked":2,"version":1})
            _, manager = post(picking_path + "/advance", {"csrf":fields["csrf"],"status":"Picking","order_version":Fields(get(picking_path)).fields["order_version"]})
            assert "Ready" in get(receipt_path)
            request = urllib.request.Request(origin + receipt_path + "/status", headers={"HX-Request":"true"})
            with client.open(request) as response:
                fragment = response.read().decode()
            assert 'id="order-status"' in fragment and '<!doctype html>' not in fragment
            # Restart the actual process; cookies, picked quantities and order must survive.
            process.terminate(); process.wait(timeout=5)
            process = start()
            assert "Ready" in get(receipt_path)
            assert "HTTP smoke restock" in get("/manager/stock")
            manager = get(picking_path)
            _, completed = post(picking_path + "/advance", {"csrf":Fields(manager).fields["csrf"],"status":"Ready","order_version":Fields(manager).fields["order_version"]})
            assert "Completed" in get(receipt_path)
            # Catalog forms use real manager authorization, stable identities and versions.
            catalog = get("/manager/catalog")
            csrf = Fields(catalog).fields["csrf"]
            _, catalog = post("/manager/catalog/categories", {"csrf":csrf,"name":"Smoke department"})
            category_form = Forms(catalog).named("name", "Smoke department", "/manager/catalog/categories/")
            category_id = category_form["action"].split("/")[-1]
            _, catalog = post("/manager/catalog/types", {"csrf":csrf,"name":"Smoke product type"})
            type_form = Forms(catalog).named("name", "Smoke product type", "/manager/catalog/types/")
            type_id = type_form["action"].split("/")[-1]
            product = {"csrf":csrf,"sku":"HTTP-SMOKE-ITEM","name":"Smoke shelf crackers",
                       "description":"Created through real HTTP forms","category_id":category_id,
                       "type_id":type_id,"icon":"bread","price":899,"sale_unit":"each","quantity_step":1}
            _, catalog = post("/manager/catalog/products", product)
            product_form = Forms(catalog).named("name", product["name"], "/manager/catalog/products/")
            product_id = product_form["action"].split("/")[-1]
            assert product_form["fields"]["sku"] == "HTTP-SMOKE-ITEM"
            assert product_form["fields"]["catalog_version"] == "1"
            _, stock_page = post("/manager/inventory", {"csrf":csrf,"product_id":product_id,
                "version":1,"delta":4,"reason":"Smoke new product stock"})
            assert "Smoke new product stock" in stock_page
            _, basket = post("/cart", {"csrf":csrf,"revision":Fields(get("/")).fields["revision"],"product_id":product_id,"quantity":1,"mode":"add","return":"cart"})
            old_quote = {key:Fields(basket).fields[key] for key in ("csrf","revision","checkout_key","quote")}
            assert "$8.99" in basket
            product.update(price=949, catalog_version=1)
            _, catalog = post(product_form["action"], product)
            _, stale = post("/checkout", old_quote)
            assert "Review the current basket" in stale and "$9.49" in stale
            fresh = {key:Fields(stale).fields[key] for key in ("csrf","revision","checkout_key","quote")}
            assert fresh["quote"] != old_quote["quote"]
            catalog_receipt_path, catalog_receipt = post("/checkout", fresh)
            assert "$9.49" in catalog_receipt and "Smoke shelf crackers" in catalog_receipt
            # Archiving removes new-sale availability, but never rewrites old receipts.
            _, catalog = post(product_form["action"] + "/archive", {"csrf":csrf,"catalog_version":2})
            assert "Smoke shelf crackers" not in get("/")
            assert "Smoke shelf crackers" in get(catalog_receipt_path)
            post(category_form["action"] + "/archive", {"csrf":csrf,"version":1})
            post(type_form["action"] + "/archive", {"csrf":csrf,"version":1})
            _, restored = post(category_form["action"] + "/restore", {"csrf":csrf,"version":2})
            restored_form = Forms(restored).named("name", "Smoke department", "/manager/catalog/categories/")
            assert restored_form["action"] == category_form["action"]
            assert restored_form["fields"]["version"] == "3"
            assert "Smoke shelf crackers" not in get("/")
            # Recovery is explicit and waits for every assigned label to be active.
            _, blocked_restore = post(product_form["action"] + "/restore", {"csrf":csrf,"catalog_version":3})
            assert 'role="alert"' in blocked_restore
            assert "Smoke shelf crackers" not in get("/")
            post(type_form["action"] + "/restore", {"csrf":csrf,"version":2})
            assert "Smoke shelf crackers" not in get("/")
            _, recovered = post(product_form["action"] + "/restore", {"csrf":csrf,"catalog_version":3})
            recovered_form = Forms(recovered).named("name", "Smoke shelf crackers", "/manager/catalog/products/")
            assert recovered_form["action"] == product_form["action"]
            assert recovered_form["fields"]["sku"] == "HTTP-SMOKE-ITEM"
            assert recovered_form["fields"]["catalog_version"] == "4"
            assert "Smoke shelf crackers" in get("/")
            process.terminate(); process.wait(timeout=5)
            process = start()
            assert "Smoke shelf crackers" in get("/")
            assert "$9.49" in get(catalog_receipt_path)
            assert "Smoke shelf crackers" in get("/manager/catalog")
            # Stock holds survive actual restart and expire once, keeping contents.
            home = get("/")
            csrf = Fields(home).fields["csrf"]
            _, basket = post("/cart", {"csrf":csrf,"revision":Fields(home).fields["revision"],"product_id":1,"quantity":2,"return":"cart"})
            assert "reserved for you" in basket and "15-MINUTE BASKET HOLD" in basket
            with sqlite3.connect(env["DATABASE_PATH"]) as db:
                basket_id, revision, deadline = db.execute("SELECT b.id,b.revision,b.hold_until FROM baskets b JOIN cart c ON c.basket_id=b.id WHERE b.synthetic=0 AND c.product_id=1").fetchone()
                available = db.execute("SELECT stock FROM products WHERE id=1").fetchone()[0]
            detail = get("/manager/baskets/" + basket_id)
            assert "Honeycrisp apples" in detail
            _, overridden = post("/manager/baskets/" + basket_id + "/items", {"csrf":csrf,"revision":revision,"product_id":1,"quantity":3,"reason":"HTTP basket override"})
            assert "HTTP basket override" in overridden
            basket = get("/cart")
            assert Fields(basket).fields["revision"] != str(revision)
            _, practices = post("/manager/baskets/practice", {"csrf":csrf})
            assert "Synthetic practice basket" in practices
            post("/manager/baskets/practice", {"csrf":csrf})
            with sqlite3.connect(env["DATABASE_PATH"]) as db:
                assert db.execute("SELECT COUNT(*) FROM baskets WHERE synthetic=1").fetchone()[0] == 2
                original_deadline = db.execute("SELECT hold_until FROM baskets WHERE id=?", (basket_id,)).fetchone()[0]
            get("/cart"); get("/manager/baskets/" + basket_id)
            with sqlite3.connect(env["DATABASE_PATH"]) as db:
                assert db.execute("SELECT hold_until FROM baskets WHERE id=?", (basket_id,)).fetchone()[0] == original_deadline
            process.terminate(); process.wait(timeout=5)
            with sqlite3.connect(env["DATABASE_PATH"]) as db:
                # This is an intentional fixture writer to a disposable current-schema DB.
                version = db.execute("SELECT MAX(version) FROM schema_version").fetchone()[0]
                db.create_function("app_schema_version", 0, lambda: version, deterministic=True)
                db.execute("UPDATE baskets SET hold_until=1 WHERE id=?", (basket_id,))
            process = start()
            expired = get("/cart")
            assert "review" in expired.lower() and "Honeycrisp apples" in expired
            with sqlite3.connect(env["DATABASE_PATH"]) as db:
                assert db.execute("SELECT stock FROM products WHERE id=1").fetchone()[0] == available + 2
                assert db.execute("SELECT reserved FROM cart WHERE basket_id=? AND product_id=1", (basket_id,)).fetchone()[0] == 0
            get("/cart"); get("/manager/stock")
            with sqlite3.connect(env["DATABASE_PATH"]) as db:
                assert db.execute("SELECT stock FROM products WHERE id=1").fetchone()[0] == available + 2
            _, renewed = post("/cart/renew", {"csrf":csrf,"revision":Fields(expired).fields["revision"]})
            assert "Reserved until" in renewed and "reserved for you" in renewed
            with sqlite3.connect(env["DATABASE_PATH"]) as db:
                assert db.execute("SELECT stock FROM products WHERE id=1").fetchone()[0] == available - 1
            assert "Keep demo bread apart &lt;script&gt;fake&lt;/script&gt;" in get(receipt_path)
            manager = get("/manager")
            post("/manager/logout", {"csrf":Fields(manager).fields["csrf"]})
            assert "Manager demo access" in get("/manager")
            print("PASS: real HTTP catalog, static asset, basket, checkout replay, manager login, stock audit, guarded picking, status fragment, process restart, collection, configurable catalog, taxonomy, stale-price reconfirmation, archive history, guarded taxonomy/product recovery and logout, persisted timed holds, scoped basket overrides, explicit practice setup, expiry/review/reacquisition, plaintext instructions and percentages")
        finally:
            process.terminate()
            process.wait(timeout=5)

if __name__ == "__main__":
    main()
