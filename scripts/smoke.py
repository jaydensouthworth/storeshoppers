#!/usr/bin/env python3
"""Run the built server and exercise real HTTP form flows with a disposable DB."""
from html.parser import HTMLParser
from http.cookiejar import CookieJar
from pathlib import Path
import os
import socket
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
            assert "The daily shop." in home and "Honeycrisp apples" in home
            assert "ZgotmplZ" not in home
            assert "htmx" in get("/static/htmx.min.js")
            csrf = Fields(home).fields["csrf"]
            _, basket = post("/cart", {"csrf":csrf,"product_id":1,"quantity":2,"mode":"add","return":"cart"})
            assert "$6.98" in basket
            fields = Fields(basket).fields
            receipt_path, receipt = post("/checkout", {key:fields[key] for key in ("csrf","revision","checkout_key")})
            assert "DEMO-" in receipt and "$6.98" in receipt and "Placed" in receipt
            replay_path, _ = post("/checkout", {key:fields[key] for key in ("csrf","revision","checkout_key")})
            assert replay_path == receipt_path
            login = get("/manager/login")
            _, manager = post("/manager/login", {"csrf":Fields(login).fields["csrf"],"password":"smoke-demo-password"})
            assert "Store workspace" in manager and "DEMO-" in manager
            fields = Fields(manager).fields
            _, manager = post("/manager/inventory", {"csrf":fields["csrf"],"product_id":1,"version":2,"delta":3,"reason":"HTTP smoke restock"})
            assert "HTTP smoke restock" in manager
            order_id = receipt_path.split('/')[-1]
            picking_path = f"/manager/orders/{order_id}"
            _, manager = post(picking_path + "/advance", {"csrf":fields["csrf"],"status":"Placed"})
            assert "Picking" in get(receipt_path)
            # Readiness is blocked until the required quantity has been picked.
            _, blocked = post(picking_path + "/advance", {"csrf":fields["csrf"],"status":"Picking"})
            assert "Pick every required item" in blocked
            _, manager = post(picking_path + "/items/1", {"csrf":fields["csrf"],"picked":2,"version":1})
            _, manager = post(picking_path + "/advance", {"csrf":fields["csrf"],"status":"Picking"})
            assert "Ready" in get(receipt_path)
            request = urllib.request.Request(origin + receipt_path + "/status", headers={"HX-Request":"true"})
            with client.open(request) as response:
                fragment = response.read().decode()
            assert 'id="order-status"' in fragment and '<!doctype html>' not in fragment
            # Restart the actual process; cookies, picked quantities and order must survive.
            process.terminate(); process.wait(timeout=5)
            process = start()
            assert "Ready" in get(receipt_path)
            assert "HTTP smoke restock" in get("/manager")
            manager = get(picking_path)
            _, completed = post(picking_path + "/advance", {"csrf":Fields(manager).fields["csrf"],"status":"Ready"})
            assert "Completed" in get(receipt_path)
            manager = get("/manager")
            post("/manager/logout", {"csrf":Fields(manager).fields["csrf"]})
            assert "Manager demo access" in get("/manager")
            print("PASS: real HTTP catalog, static asset, basket, checkout replay, manager login, stock audit, guarded picking, status fragment, process restart, collection and logout")
        finally:
            process.terminate()
            process.wait(timeout=5)

if __name__ == "__main__":
    main()
