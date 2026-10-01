#!/usr/bin/env python3
"""Real HTTP inventory/activity filters, safe adjustment drafts, scope and restart."""
from http.cookiejar import CookieJar
from pathlib import Path
import os
import re
import socket
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request
from smoke import Fields, Forms

ROOT = Path(__file__).resolve().parent.parent


def exercise(demo):
    with tempfile.TemporaryDirectory(prefix="shopper-stock-tools-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=f"{directory}/shop.db", MANAGER_PASSWORD="stock-smoke-password",
                   DEMO_MODE="true" if demo else "false")
        def client():
            return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))
        owner, stranger = client(), client()
        def get(path, who=owner, htmx=False):
            request = urllib.request.Request(origin+path, headers={"HX-Request":"true"} if htmx else {})
            with who.open(request, timeout=5) as response:
                return response.read().decode()
        def post(path, fields, who=owner, htmx=False):
            request = urllib.request.Request(origin+path, data=urllib.parse.urlencode(fields).encode(),
                headers={"Origin":origin, **({"HX-Request":"true"} if htmx else {})})
            with who.open(request, timeout=5) as response:
                return response.geturl().removeprefix(origin), response.read().decode(), response.headers
        def start():
            process = subprocess.Popen([str(ROOT/"bin/shop")],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
            for _ in range(100):
                try:
                    if get("/healthz")=="ok\n":
                        return process
                except OSError:
                    if process.poll() is not None:
                        raise RuntimeError(process.stderr.read().decode())
                    time.sleep(.05)
            process.terminate()
            raise RuntimeError("Stock test server did not start")
        process = start()
        try:
            def place(who):
                fields=Fields(get("/",who)).fields
                _, basket, _=post("/cart",{"csrf":fields["csrf"],"revision":fields["revision"],"product_id":1,"quantity":1,"mode":"add","return":"cart"},who)
                fields=Fields(basket).fields
                path, receipt, _=post("/checkout",{key:fields[key] for key in ("csrf","revision","checkout_key","quote")},who)
                return path, re.search(r'DEMO-[A-Z0-9]+',receipt).group()
            mine, mine_ref=place(owner)
            foreign, foreign_ref=place(stranger)
            login=get("/manager/login")
            post("/manager/login",{"csrf":Fields(login).fields["csrf"],"password":"stock-smoke-password"})
            stock=get("/manager/stock")
            assert len(re.findall(r'id="stock-product-\d+"',stock))==12
            assert 'action="/manager/inventory"' not in stock
            assert 'href="/manager/stock?page=2#inventory"' in stock
            page=get("/manager/stock?page=999999")
            assert "Page 6 of 6" in page
            path="/manager/stock?q=Honeycrisp&stock=available&product=1&sort=reserved"
            selected=get(path)
            form=Forms(selected).named("product_id","1","/manager/inventory")
            original_version=form["fields"]["version"]
            values=dict(form["fields"],delta="bad",reason="Preserved stock draft <script>safe</script>")
            _, draft, _=post("/manager/inventory",values,htmx=True)
            assert "<!doctype html>" not in draft and "Not saved." in draft
            assert "&lt;script&gt;safe&lt;/script&gt;" in draft
            retry=Forms(draft).named("product_id","1","/manager/inventory")["fields"]
            assert retry["version"]==original_version and retry["q"]=="Honeycrisp"
            retry.update(delta="2",reason="Real HTTP stock adjustment")
            _, saved, headers=post("/manager/inventory",retry,htmx=True)
            assert "Inventory adjusted." in saved and "Real HTTP stock adjustment" in saved
            assert "product=1" in headers["HX-Push-Url"] and "q=Honeycrisp" in headers["HX-Push-Url"]
            _, conflict, _=post("/manager/inventory",retry)
            assert "This changed since you opened it." in conflict and "Not saved." in conflict
            closed=get("/manager/stock?q=Honeycrisp&stock=available&sort=reserved")
            assert 'action="/manager/inventory"' not in closed
            activity=get("/manager/stock?view=activity&action=order-placed&activity_product=1")
            assert mine_ref in activity and (foreign_ref not in activity if demo else foreign_ref in activity)
            if demo:
                assert f'href="/manager{foreign}"' not in activity
            history=get("/manager/stock?view=activity&action=stock&activity_product=1")
            assert "Real HTTP stock adjustment" in history and "Order placed</span>" not in history
            invalid=get("/manager/stock?view=activity&from=2026-10-02&to=2026-10-01")
            assert "The start date must be on or before" in invalid and "Real HTTP stock adjustment" not in invalid
            assert "Honeycrisp apples" in get(mine)
            if not demo:
                process.terminate();process.wait(timeout=5);process=start()
                assert "Real HTTP stock adjustment" in get("/manager/stock?view=activity&action=stock")
                assert "Honeycrisp apples" in get(mine)
        finally:
            process.terminate()
            process.wait(timeout=5)
            process.stderr.close()


if __name__=="__main__":
    exercise(False)
    exercise(True)
    print("PASS: stock pagination/filters, one selected adjustment, HTML/HTMX drafts, stale retry rejection, activity filters, owned demo activity links, immutable receipt and normal restart persistence")
