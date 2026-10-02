#!/usr/bin/env python3
"""Follow rendered basket forms against a real process, including no-JS drafts."""
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


class BasketForm(HTMLParser):
    def __init__(self, text):
        super().__init__(convert_charrefs=True)
        self.active = False
        self.action = None
        self.fields = {}
        self.buttons = {}
        self.textarea = None
        self.feed(text)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "form" and attrs.get("id") == "customer-basket":
            assert not self.active
            self.active = True
            self.action = attrs["action"]
        elif tag == "form" and self.active:
            raise AssertionError("Basket must not contain nested forms")
        elif self.active and tag == "input" and attrs.get("name") and "disabled" not in attrs:
            self.fields[attrs["name"]] = attrs.get("value", "")
        elif self.active and tag == "textarea":
            self.textarea = attrs["name"]
            self.fields[self.textarea] = ""
        elif self.active and tag == "button" and attrs.get("id"):
            self.buttons[attrs["id"]] = attrs

    def handle_data(self, data):
        if self.textarea:
            self.fields[self.textarea] += data

    def handle_endtag(self, tag):
        if tag == "textarea":
            self.textarea = None
        elif tag == "form":
            self.active = False

    def submission(self, button):
        attrs = self.buttons[button]
        assert "disabled" not in attrs, f"Cannot submit disabled {button}"
        fields = dict(self.fields)
        if attrs.get("name"):
            fields[attrs["name"]] = attrs.get("value", "")
        return attrs.get("formaction", self.action), fields


class HiddenFields(HTMLParser):
    def __init__(self, text):
        super().__init__()
        self.fields = {}
        self.feed(text)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "input" and attrs.get("type") == "hidden":
            self.fields.setdefault(attrs.get("name"), attrs.get("value", ""))


def main():
    with tempfile.TemporaryDirectory(prefix="shopper-cart-drafts-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        database = f"{directory}/shop.db"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=database, MANAGER_PASSWORD="smoke-demo-password")
        process = subprocess.Popen([str(ROOT / "bin/shop")], env=env,
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            for _ in range(100):
                try:
                    with urllib.request.urlopen(origin + "/healthz", timeout=1) as response:
                        assert response.read() == b"ok\n"
                    break
                except (OSError, AssertionError):
                    assert process.poll() is None, "server exited"
                    time.sleep(.05)
            else:
                raise AssertionError("server did not become ready")

            for enhanced in (False, True):
                jar = CookieJar()
                client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

                def get(path):
                    with client.open(origin + path, timeout=5) as response:
                        return response.read().decode()

                def post(path, fields):
                    headers = {"Origin": origin}
                    if enhanced:
                        headers["HX-Request"] = "true"
                    req = urllib.request.Request(origin + path, data=urllib.parse.urlencode(fields).encode(), headers=headers)
                    with client.open(req, timeout=5) as response:
                        body = response.read().decode()
                        redirect = response.headers.get("HX-Redirect")
                        return redirect or response.geturl().removeprefix(origin), body, redirect

                def submit(form, button):
                    return post(*form.submission(button))

                home = HiddenFields(get("/")).fields
                for product_id, quantity in ((1, 1), (2, 2)):
                    _, body, _ = post("/cart", {"csrf": home["csrf"], "revision": home["revision"],
                                                "product_id": product_id, "quantity": quantity, "return": "cart"})
                    home = HiddenFields(body).fields
                form = BasketForm(body)
                note = "Fake basket note <script>plain text</script>\nBread on top & dry"
                form.fields["instructions"] = note
                form.fields["quantity-1"] = "3"
                form.fields["quantity-2"] = "4"
                path, body, redirect = submit(form, "update-1")
                assert path == "/cart" and not redirect
                form = BasketForm(body)
                assert form.fields["instructions"] == note
                assert form.fields["quantity-2"] == "4"
                assert "&lt;script&gt;plain text&lt;/script&gt;" in body
                with sqlite3.connect(database) as db:
                    owner = next(c.value for c in jar if c.name == "shop_session")
                    basket_id = db.execute("SELECT id FROM baskets WHERE owner_session_id=? AND synthetic=0", (owner,)).fetchone()[0]
                    assert db.execute("SELECT quantity FROM cart WHERE basket_id=? AND product_id=2", (basket_id,)).fetchone()[0] == 2
                _, body, _ = submit(form, "place-order")
                assert "Some quantities have not been saved" in body
                assert BasketForm(body).fields["instructions"] == note
                form = BasketForm(body)
                _, body, _ = submit(form, "update-2")
                form = BasketForm(body)
                assert form.fields["instructions"] == note
                with sqlite3.connect(database) as db:
                    # Only this disposable fixture connection may bypass the writer guard.
                    version = db.execute("SELECT MAX(version) FROM schema_version").fetchone()[0]
                    db.create_function("app_schema_version", 0, lambda: version, deterministic=True)
                    db.execute("UPDATE baskets SET hold_until=? WHERE id=?", (int(time.time()) - 1, basket_id))
                _, body, _ = submit(form, "place-order")
                form = BasketForm(body)
                assert form.fields["instructions"] == note
                assert "disabled" in form.buttons["place-order"]
                _, body, redirect = submit(form, "renew-basket")
                assert not redirect, "renew must not redirect away a submitted draft"
                form = BasketForm(body)
                assert form.fields["instructions"] == note
                assert "disabled" not in form.buttons["place-order"]
                stale_path, stale_values = form.submission("update-1")
                checkout_path, checkout_values = form.submission("place-order")
                receipt, body, redirect = post(checkout_path, checkout_values)
                assert receipt.startswith("/orders/")
                if redirect:
                    body = get(redirect)
                assert "Bread on top &amp; dry" in body
                replay, _, _ = post(checkout_path, checkout_values)
                assert replay == receipt
                empty = get("/cart")
                assert "instructions" not in BasketForm(empty).fields
                fresh = HiddenFields(get("/")).fields
                _, body, _ = post("/cart", {"csrf": fresh["csrf"], "revision": fresh["revision"],
                                            "product_id": 1, "quantity": 1, "return": "cart"})
                assert BasketForm(body).fields["instructions"] == ""
                _, body, _ = post(stale_path, stale_values)
                assert BasketForm(body).fields["instructions"] == ""
                assert BasketForm(body).fields["quantity-1"] == "1"
                with sqlite3.connect(database) as db:
                    assert db.execute("SELECT COUNT(*) FROM orders WHERE session_id=?", (owner,)).fetchone()[0] == 1
                    assert db.execute("SELECT instructions FROM orders WHERE session_id=?", (owner,)).fetchone()[0] == note
            print("PASS: actual basket-form HTML/HTMX quantity drafts, plaintext notes, unapplied-quantity checkout guard, expiry review, renewal, replay and next-basket separation")
        finally:
            process.terminate()
            process.wait(timeout=5)


if __name__ == "__main__":
    main()
