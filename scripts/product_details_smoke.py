#!/usr/bin/env python3
"""Build current source; exercise product details through disposable real HTTP."""
from contextlib import closing
from html.parser import HTMLParser
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

from order_overrides_smoke import Page
from smoke import Fields

ROOT = Path(__file__).resolve().parent.parent


class DetailForms(HTMLParser):
    """Collect browser-submitted controls, including textarea/select values."""
    def __init__(self, markup):
        super().__init__()
        self.forms = []
        self.current = None
        self.textarea = None
        self.select = None
        self.feed(markup)

    def handle_starttag(self, tag, pairs):
        attrs = dict(pairs)
        if tag == "form":
            self.current = {"action": attrs.get("action", ""), "fields": {}}
        if self.current is None:
            return
        name = attrs.get("name")
        if tag == "input" and name and "disabled" not in attrs:
            if attrs.get("type") in ("checkbox", "radio") and "checked" not in attrs:
                return
            self.current["fields"][name] = attrs.get("value", "")
        elif tag == "textarea" and name:
            self.textarea = name
            self.current["fields"][name] = ""
        elif tag == "select" and name:
            self.select = name
        elif tag == "option" and self.select:
            if self.select not in self.current["fields"] or "selected" in attrs:
                self.current["fields"][self.select] = attrs.get("value", "")

    def handle_data(self, data):
        if self.current is not None and self.textarea:
            self.current["fields"][self.textarea] += data

    def handle_endtag(self, tag):
        if tag == "textarea":
            self.textarea = None
        elif tag == "select":
            self.select = None
        elif tag == "form" and self.current is not None:
            self.forms.append(self.current)
            self.current = None

    def action(self, path):
        for form in self.forms:
            if urllib.parse.urlsplit(form["action"]).path == path:
                return form["fields"]
        raise AssertionError(f"Missing form for {path}")


def main():
    with tempfile.TemporaryDirectory(prefix="shop-details-") as directory:
        binary = Path(directory) / "shop"
        go = ROOT / ".tools/go/bin/go"
        build_env = dict(os.environ)
        build_env.setdefault("GOCACHE", "/tmp/instore-gocache")
        build_env.setdefault("GOPATH", "/tmp/instore-gopath")
        subprocess.run([str(go) if go.exists() else "go", "build", "-o", str(binary), "./cmd/shop"],
                       cwd=ROOT, env=build_env, check=True, timeout=120)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        db_path = Path(directory) / "shop.db"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=str(db_path), MANAGER_PASSWORD="details-smoke-password", DEMO_MODE="true")
        client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

        def request(path, fields=None, hx=False, status=200):
            headers = {"Origin": origin}
            if hx:
                headers["HX-Request"] = "true"
            data = None if fields is None else urllib.parse.urlencode(fields).encode()
            try:
                response = client.open(urllib.request.Request(origin + path, data=data, headers=headers), timeout=5)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                assert response.code == status, (path, response.code, response.read().decode())
                body = response.read().decode()
                if status == 200 and path != "/healthz":
                    Page(body)  # IDs, label/ARIA targets, nested forms, escaping.
                return response.geturl().removeprefix(origin), body

        def start():
            process = subprocess.Popen([str(binary)], env=env, stdout=subprocess.DEVNULL,
                                       stderr=subprocess.PIPE, text=True)
            for _ in range(100):
                if process.poll() is not None:
                    raise AssertionError("details server exited: " + process.stderr.read())
                try:
                    assert request("/healthz")[1] == "ok\n"
                    return process
                except OSError:
                    time.sleep(.05)
            process.terminate()
            process.wait(timeout=5)
            raise AssertionError("details server not ready: " + process.stderr.read())

        def stop(process):
            process.terminate()
            process.wait(timeout=5)
            process.stderr.close()

        process = start()
        try:
            with closing(sqlite3.connect(db_path)) as db:
                assert db.execute("SELECT COUNT(*) FROM products").fetchone()[0] == 72
                assert db.execute("SELECT MAX(version) FROM schema_version").fetchone()[0] == 15
            _, deodorant = request("/products/71")
            assert "Everyday deodorant" in deodorant and 'id="nutrition-title"' not in deodorant
            _, razors = request("/products/72")
            assert "Three-blade razors" in razors and 'id="nutrition-title"' not in razors
            _, home = request("/")
            assert "/products/1" in home and "/products/71" in home
            request("/products/999999", status=404)
            request("/products/not-an-id", status=404)
            request("/manager/catalog/products/1", {"csrf": "wrong"}, status=403)
            csrf = Fields(home).fields["csrf"]
            _, manager = request("/manager/login", {"csrf": csrf, "password": "details-smoke-password"})
            csrf = Fields(manager).fields["csrf"]

            # Sample offers require the original catalog identities, so install
            # them before deliberately editing the apples' presentation.
            _, preview = request("/manager/promotions/examples")
            promotion_fields = DetailForms(preview).action("/manager/promotions/examples")
            request("/manager/promotions/examples", dict(promotion_fields, confirm="1"))

            editor_path = "/manager/catalog/products/1"
            _, editor = request("/manager/catalog?edit=1")
            fields = DetailForms(editor).action(editor_path)
            details = dict(details_kind="food", details_body='Crisp <script>demo</script> & juicy apples.',
                           package_label="One loose apple", package_details="Sold individually. Demo nutrition only.",
                           nutrition_enabled="1", nutrition_serving="100 g", nutrition_energy="52",
                           nutrition_fat="0.2", nutrition_carbs="13.8", nutrition_protein="0.3", nutrition_sodium="1")
            fields.update(details)
            invalid = dict(fields, nutrition_fat="0.25", nutrition_sodium="invalid")
            _, draft = request(editor_path, invalid, hx=True)
            draft_fields = DetailForms(draft).action(editor_path)
            assert draft_fields["nutrition_fat"] == "0.25" and draft_fields["nutrition_sodium"] == "invalid"
            assert draft_fields["details_body"] == details["details_body"] and "<!doctype html>" not in draft.lower()
            assert "Please check" in draft and "<script>demo</script>" not in draft
            request(editor_path, fields)
            _, current = request("/products/1")
            assert "&lt;script&gt;demo&lt;/script&gt;" in current and "100 g" in current and "13.8" in current
            _, stale = request(editor_path, dict(fields, details_body="Stale draft must not win"))
            assert "changed" in Page(stale).text.lower()
            assert DetailForms(stale).action(editor_path)["details_body"] == details["details_body"]

            context = "/products/1?q=Honeycrisp&category=Produce&sales=1&featured=1"
            _, product = request(context)
            assert "$2.49" in product and "$3.49" in product
            add = DetailForms(product).action("/cart")
            assert add["return"] == "product" and add["q"] == "Honeycrisp" and add["sales"] == "1"
            route, product = request("/cart", dict(add, quantity="2"))
            destination = urllib.parse.urlsplit(route)
            assert destination.path == "/products/1"
            assert urllib.parse.parse_qs(destination.query) == dict(q=["Honeycrisp"], category=["Produce"], sales=["1"], featured=["1"])
            add = DetailForms(product).action("/cart")
            _, updated = request("/cart", dict(add, quantity="1"), hx=True)
            assert "<!doctype html>" not in updated.lower() and DetailForms(updated).action("/cart")["return"] == "product"
            _, stale_add = request("/cart", dict(add, quantity="1"), hx=True)
            assert "changed" in Page(stale_add).text.lower()
            _, basket = request("/cart")
            assert "$7.47" in basket
            checkout = DetailForms(basket).action("/checkout")
            receipt_route, receipt = request("/checkout", checkout)
            assert "$7.47" in receipt

            # New weighted products remain browseable without counted checkout.
            _, new_editor = request("/manager/catalog?tab=new")
            weighted = DetailForms(new_editor).action("/manager/catalog/products")
            weighted.update(sku="DETAIL-WEIGHED-DEMO", name="Smoke weighed almonds", description="Future weighted item",
                            category_id="1", type_id="0", icon="leaf", price="1599", sale_unit="g", quantity_step="100",
                            details_kind="food", details_body="Displayed by weight", package_label="Per kilogram")
            weighted.pop("nutrition_enabled", None)
            weighted_route, _ = request("/manager/catalog/products", weighted)
            weighted_id = urllib.parse.parse_qs(urllib.parse.urlsplit(weighted_route).query)["edit"][0]
            _, weighted_page = request("/products/" + weighted_id)
            assert "Smoke weighed almonds" in weighted_page
            weighted_cart = [f for f in DetailForms(weighted_page).forms if f["action"] == "/cart"]
            assert len(weighted_cart) == 1
            assert weighted_cart[0]["fields"]["product_id"] == weighted_id
            assert 'min="100" max="100000" step="100"' in weighted_page
            # New catalog products start sold out; quantity and submit controls
            # remain disabled until the separate audited stock action.
            assert 'id="detail-quantity"' in weighted_page and 'required disabled' in weighted_page
            assert 'id="detail-add-' + weighted_id + '"' in weighted_page
            assert "Requested grams reserve stock" in weighted_page

            # Existing example installation is an informative, non-mutating page.
            _, installed = request("/manager/catalog/examples")
            assert "already" in Page(installed).text.lower()
            stop(process)
            process = start()
            _, persisted = request("/products/1")
            assert "&lt;script&gt;demo&lt;/script&gt;" in persisted and "13.8" in persisted
            assert "$7.47" in request(receipt_route)[1]

            # Clearing the opt-in clears nutrition while retaining nonfood details.
            _, editor = request("/manager/catalog?edit=1")
            fields = DetailForms(editor).action(editor_path)
            fields.pop("nutrition_enabled", None)
            fields["details_kind"] = "nonfood"
            fields["details_body"] = "Nonfood classification smoke check"
            request(editor_path, fields)
            _, cleared = request("/products/1")
            assert "Nonfood classification smoke check" in cleared and 'id="nutrition-title"' not in cleared
            _, editor = request("/manager/catalog?edit=" + weighted_id)
            archive_path = f"/manager/catalog/products/{weighted_id}/archive"
            request(archive_path, DetailForms(editor).action(archive_path))
            request("/products/" + weighted_id, status=404)

            _, reset = request("/manager/demo/reset")
            request("/manager/demo/reset", {"csrf": Fields(reset).fields["csrf"], "confirm": "reset-shared-demo"})
            _, restored = request("/products/71")
            assert "Everyday deodorant" in restored
            assert "Nonfood classification smoke check" not in request("/products/1")[1]
            with closing(sqlite3.connect(db_path)) as db:
                assert db.execute("SELECT COUNT(*) FROM products").fetchone()[0] == 72
                assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
                assert not db.execute("PRAGMA foreign_key_check").fetchall()
            print("Product details HTTP smoke passed: fresh build, 72 seed products, detail links, escaped food/nonfood details, "
                  "nutrition validation and clearing, stale drafts, sales and filtered cart returns, HTMX retry safety, "
                  "weighted browsing, archive 404, restart persistence, immutable receipt and explicit reset")
        finally:
            if process.poll() is None:
                stop(process)


if __name__ == "__main__":
    main()
