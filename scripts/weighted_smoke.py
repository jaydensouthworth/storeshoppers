#!/usr/bin/env python3
"""Real-process weighted HTTP smoke; fake disposable data, no browser/visual QA.

Run after building bin/shop. All mutations use rendered application forms;
SQLite is opened read-only solely to verify stock, receipts and persistence.
"""
from html.parser import HTMLParser
from http.cookiejar import CookieJar
from pathlib import Path
import copy
import json
import os
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
PASSWORD = "weighted-smoke-demo-only"


class Page(HTMLParser):
    """Parse successful form controls and check labels, IDs and form structure."""
    def __init__(self, html):
        super().__init__()
        self.html, self.text = html, ""
        self.forms, self.all_forms, self.controls = {}, [], {}
        self.ids, self.refs = set(), []
        self.current = self.select = self.textarea = None
        self.problem = False
        self.feed(html)
        assert self.current is None, "unclosed form"
        assert all(ref in self.ids for ref in self.refs), ("missing referenced ID", self.refs, self.ids)
        assert "ZgotmplZ" not in html
        self.text = " ".join(self.text.split())

    def handle_starttag(self, tag, pairs):
        a = dict(pairs)
        if "id" in a:
            assert a["id"] not in self.ids, ("duplicate ID", a["id"])
            self.ids.add(a["id"])
            self.controls[a["id"]] = a
        if tag == "label" and "for" in a:
            self.refs.append(a["for"])
        for attr in ("aria-labelledby", "aria-describedby"):
            self.refs.extend(a.get(attr, "").split())
        if {"notice", "error"}.issubset(a.get("class", "").split()) and "hidden" not in a:
            self.problem = True
        if tag == "form":
            assert self.current is None, "nested form"
            self.current = {"action": a.get("action", ""), "method": a.get("method", "get"),
                            "fields": {}, "choices": {}, "keys": []}
            if a.get("id"):
                self.current["keys"].append(a["id"])
        if self.current is None:
            return
        fields, choices = self.current["fields"], self.current["choices"]
        name = a.get("name")
        if tag in ("input", "select", "textarea") and name and "disabled" not in a:
            kind = a.get("type", "")
            if kind in ("radio", "checkbox"):
                choices.setdefault(name, []).append(a.get("value", "on"))
                if "checked" in a:
                    fields[name] = a.get("value", "on")
            else:
                fields[name] = a.get("value", "")
            if tag == "textarea":
                self.textarea = name
            if tag == "select":
                self.select = name
                choices[name] = []
        if tag == "option" and self.select:
            choices[self.select].append(a.get("value", ""))
            if len(choices[self.select]) == 1 or "selected" in a:
                fields[self.select] = a.get("value", "")
        if tag == "button" and a.get("id"):
            self.current["keys"].append(a["id"])

    def handle_endtag(self, tag):
        if tag == "select":
            self.select = None
        if tag == "textarea":
            self.textarea = None
        if tag == "form" and self.current is not None:
            self.all_forms.append(self.current)
            for key in self.current["keys"]:
                self.forms[key] = self.current
            self.current = None

    def handle_data(self, data):
        self.text += " " + data
        if self.textarea and self.current:
            self.current["fields"][self.textarea] += data

    def action(self, path):
        forms = [form for form in self.all_forms if form["action"] == path]
        assert len(forms) == 1, (path, len(forms))
        return forms[0]

    def contains(self, *strings):
        for string in strings:
            assert string in self.text, ("missing page text", string, self.text)


class Client:
    def __init__(self, origin):
        self.origin = origin
        self.jar = CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(self.jar))

    def request(self, path, fields=None, hx=False):
        headers = {"Origin": self.origin} if fields is not None else {}
        if hx:
            headers["HX-Request"] = "true"
        data = None if fields is None else urllib.parse.urlencode(fields).encode()
        request = urllib.request.Request(self.origin + path, data=data, headers=headers)
        try:
            response = self.opener.open(request, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, response.headers, response.geturl().removeprefix(self.origin), response.read().decode()

    def get(self, path, hx=False):
        status, _, url, html = self.request(path, hx=hx)
        assert status == 200, (path, status, html)
        return url, Page(html)

    def post(self, form, changes=None, hx=False, problem=False):
        fields = dict(form["fields"], **(changes or {}))
        for name, options in form["choices"].items():
            if name in fields:
                assert str(fields[name]) in options, ("unrendered choice", name, fields[name], options)
        if form["method"].lower() == "get":
            return self.get(form["action"] + "?" + urllib.parse.urlencode(fields), hx)
        status, _, url, html = self.request(form["action"], fields, hx)
        assert status == 200, (form["action"], status, html)
        page = Page(html)
        assert page.problem == problem, page.text
        return url, page

    def submit(self, page, key, changes=None, hx=False, problem=False):
        return self.post(page.forms[key], changes, hx, problem)

    def login(self):
        _, page = self.get("/manager/login")
        self.post(page.action("/manager/login"), {"password": PASSWORD})


def main():
    assert (ROOT / "bin/shop").is_file(), "Build bin/shop before running this smoke"
    with tempfile.TemporaryDirectory(prefix="shopper-weighted-smoke-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        db_path = Path(directory) / "shop.db"
        log_path = Path(directory) / "server.log"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=str(db_path), MANAGER_PASSWORD=PASSWORD,
                   DEMO_MODE="true", GOMAXPROCS="2")
        owner, other = Client(origin), Client(origin)
        process = None

        def query(sql, args=()):
            with sqlite3.connect(db_path.as_uri() + "?mode=ro", uri=True) as db:
                db.execute("PRAGMA query_only=ON")
                return db.execute(sql, args).fetchall()

        def stock(pid):
            return query("SELECT stock FROM products WHERE id=?", (pid,))[0][0]

        def order_state(oid):
            return (query("SELECT * FROM orders WHERE id=?", (oid,)),
                    query("SELECT * FROM working_order_items WHERE order_id=? ORDER BY id", (oid,)),
                    query("SELECT * FROM order_events WHERE order_id=? ORDER BY id", (oid,)),
                    query("SELECT id,stock,version FROM products ORDER BY id"))

        def receipt_snapshot(oid):
            return (query("SELECT total FROM orders WHERE id=?", (oid,)),
                    query("SELECT product_id,name,sku,sale_unit,price_basis,quantity_step,quantity,price,subtotal "
                          "FROM order_items WHERE order_id=? ORDER BY product_id", (oid,)))

        def line(oid, pid):
            return query("SELECT id,quantity,allocated_quantity,picked_quantity,measurement_confirmed,price "
                         "FROM working_order_items WHERE order_id=? AND product_id=?", (oid, pid))[0]

        def start():
            with log_path.open("ab") as log:
                proc = subprocess.Popen([str(ROOT / "bin/shop")], env=env, stdout=log, stderr=log)
            try:
                for _ in range(100):
                    try:
                        status, _, _, text = owner.request("/healthz")
                        if status == 200 and text == "ok\n":
                            return proc
                    except OSError:
                        pass
                    assert proc.poll() is None, "server exited during startup"
                    time.sleep(.05)
                raise AssertionError("server did not become ready")
            except BaseException:
                proc.terminate()
                proc.wait(timeout=5)
                raise

        def stop():
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=5)

        def create_weighted(name, sku, rate, step):
            _, page = owner.get("/manager/catalog?tab=new")
            form = page.forms["create-product"]
            category = next(v for v in form["choices"]["category_id"] if v)
            owner.post(form, {"name": name, "sku": sku, "description": "Disposable fake weighted smoke product",
                              "category_id": category, "type_id": "0", "icon": "apple",
                              "sale_unit": "g", "price": str(rate), "quantity_step": str(step)})
            pid = query("SELECT id FROM products WHERE sku=?", (sku,))[0][0]
            assert stock(pid) == 0
            _, page = owner.get(f"/manager/stock?product={pid}")
            assert page.controls[f"delta-{pid}"]["max"] == "1000000"
            owner.submit(page, f"save-stock-{pid}", {"delta": "10000", "reason": "Fake weighted stock"})
            assert query("SELECT sale_unit,price_basis,quantity_step,price,stock FROM products WHERE id=?", (pid,))[0] == ("g", 1000, step, rate, 10000)
            return pid

        def add_to_cart(pid, quantity):
            _, page = owner.get(f"/products/{pid}")
            return owner.submit(page, f"detail-add-{pid}", {"quantity": str(quantity), "return": "cart"})[1]

        def preview(page, lid, actual, disposition="", hx=False):
            return owner.submit(page, f"preview-actual-{lid}", {
                "actual": str(actual), "disposition": disposition,
                "reason": "Private fake scale note <weighted-smoke>"}, hx=hx)[1]

        try:
            process = start()
            owner.login()
            pear = create_weighted("Smoke weighed pears", "WEIGHT-PEAR", 349, 100)
            plum = create_weighted("Smoke weighed plums", "WEIGHT-PLUM", 499, 250)
            counted_before = stock(1)
            _, product = owner.get(f"/products/{pear}")
            gram_form = product.forms[f"detail-add-{pear}"]
            gram_input = next(a for a in product.controls.values() if a.get("name") == "quantity")
            assert (gram_input["min"], gram_input["max"], gram_input["step"]) == ("100", "100000", "100")
            _, basket = owner.post(gram_form, {"quantity": "500", "return": "cart"})
            basket.contains("500 g", "$1.75", "reserved for you", "ESTIMATE")
            assert stock(pear) == 9500
            basket = add_to_cart(1, 2)
            basket.contains("2 product lines", "$8.73")
            held = (stock(pear), stock(1))
            assert held == (9500, counted_before - 2)
            assert query("SELECT quantity,reserved FROM cart WHERE product_id=?", (pear,)) == [(500, 500)]
            checkout = copy.deepcopy(basket.forms["place-order"])
            receipt, page = owner.post(checkout)
            oid = int(receipt.rsplit("/", 1)[1])
            assert (stock(pear), stock(1)) == held, "checkout deducted held stock twice"
            assert owner.post(checkout)[0] == receipt
            assert not query("SELECT * FROM cart WHERE product_id=?", (pear,))
            original = receipt_snapshot(oid)
            assert original[0] == [(873,)]
            assert original[1][-1][3:] == ("g", 1000, 100, 500, 349, 175)
            page.contains("ORIGINAL PLACED RECEIPT", "500 g", "$8.73", "0 of 2 product lines picked")
            ticket = "/manager" + receipt
            _, page = owner.get(ticket)
            lid = line(oid, pear)[0]
            assert f"mark-picked-{lid}" not in page.forms
            assert line(oid, pear)[1:] == (500, 500, 0, 0, 349)

            # Add a second weighed line using its rendered per-product quantity.
            _, page = owner.submit(page, "order-add-search-button", {"product_q": "WEIGHT-PLUM"})
            picker = page.forms["order-add-item"]
            assert str(plum) in picker["choices"]["product_id"]
            assert "quantity" not in picker["fields"]
            assert picker["fields"][f"quantity_{plum}"] == "250"
            _, page = owner.post(picker, {"product_id": str(plum), f"quantity_{plum}": "500"})
            assert stock(plum) == 9500 and receipt_snapshot(oid) == original

            # A later catalog edit must not replace the placed or working rate.
            _, editor = owner.get(f"/manager/catalog?edit={pear}")
            owner.submit(editor, f"save-product-{pear}", {"name": "Changed catalog pears", "price": "999"})
            assert query("SELECT price FROM products WHERE id=?", (pear,)) == [(999,)]
            _, page = owner.get(ticket)
            before = order_state(oid)
            review = preview(page, lid, 527, hx=True)
            review.contains("Not saved yet", "500 g", "527 g", "$3.49 per kg", "$1.75", "$1.84", "+$0.09", "Reserve 27 g more")
            assert "<!doctype html>" not in review.html.lower()
            assert order_state(oid) == before, "preview changed stock/order/audit"
            confirm = copy.deepcopy(review.forms[f"confirm-actual-{lid}"])
            _, rejected = owner.post(confirm, {"actual": "528"}, hx=True, problem=True)
            rejected.contains("Submitted actual: 528 g")
            assert f"confirm-actual-{lid}" not in rejected.forms and order_state(oid) == before

            # Scope is checked before malformed payloads can disclose private data.
            other.login()
            _, foreign_page = other.get("/manager")
            foreign_tokens = {form["fields"]["csrf"] for form in foreign_page.all_forms if "csrf" in form["fields"]}
            assert len(foreign_tokens) == 1
            foreign_csrf = foreign_tokens.pop()
            for path in (receipt, receipt + "/status", ticket):
                status, _, _, body = other.request(path)
                assert status == 404 and "Smoke weighed pears" not in body
            for suffix in ("preview", "confirm"):
                status, _, _, body = other.request(f"{ticket}/lines/{lid}/weight/{suffix}",
                    dict(confirm["fields"], csrf=foreign_csrf, actual="invalid"), hx=True)
                assert status == 404 and "Private fake scale note" not in body and "Smoke weighed pears" not in body
            assert order_state(oid) == before

            _, page = owner.post(confirm, hx=True)
            assert line(oid, pear)[1:] == (500, 527, 527, 1, 349)
            assert stock(pear) == 9473 and receipt_snapshot(oid) == original
            confirmed = order_state(oid)
            owner.post(confirm)  # HTML retry after an HTMX confirmation.
            assert order_state(oid) == confirmed, "exact replay changed stock/order/audit"
            page.contains("33% shopped", "527 g measured")

            # A valid review becomes stale after another line changes the order.
            _, page = owner.get(ticket)
            stale = copy.deepcopy(preview(page, lid, 530).forms[f"confirm-actual-{lid}"])
            _, page = owner.get(ticket)
            _, page = owner.submit(page, "save-picked-1", {"picked": "1"})
            after_pick = order_state(oid)
            status, headers, _, body = owner.request(stale["action"], stale["fields"], hx=True)
            rejected = Page(body)
            assert status == 200 and headers.get("X-Shop-Error") == "stale-version"
            rejected.contains("Submitted actual: 530 g")
            assert rejected.problem and f"confirm-actual-{lid}" not in rejected.forms
            assert order_state(oid) == after_pick
            page.contains("33% shopped", "Honeycrisp apples: 1 units picked", "unmeasured, no grams fulfilled", "totaling $5.33")
            # Product-line progress must not add grams to the counted partial pick.
            assert "disabled" in page.controls["advance-order"]
            _, page = owner.submit(page, "order-finish-partial", {
                "remainder": "unavailable", "disposition": "restock", "reason": "Fake unfulfilled quantities returned"})
            assert query("SELECT status,total,final_total,completion_kind FROM orders WHERE id=?", (oid,))[0] == ("Completed", 873, 533, "partial")
            assert (stock(pear), stock(plum), stock(1)) == (9473, 10000, counted_before - 1)
            assert query("SELECT product_id,picked_quantity,unavailable_quantity FROM working_order_items WHERE order_id=? ORDER BY product_id", (oid,)) == [(1, 1, 1), (pear, 527, 0), (plum, 0, 500)]
            _, receipt_page = owner.get(receipt)
            receipt_page.contains("33% shopped", "1 of 3 product lines picked", "FINAL TOTAL", "$5.33", "$8.73", "500 g unavailable")
            assert receipt_snapshot(oid) == original
            _, activity = other.get("/manager/stock?view=activity")
            assert "Private fake scale note" not in activity.text
            assert not query("SELECT id FROM adjustments WHERE reason LIKE '%Private fake scale note%'")

            # Below-allocation reviews require a disposition; restock and writeoff
            # have different stock effects. A measured zero may finalize at $0.
            basket = add_to_cart(pear, 500)
            receipt_zero, _ = owner.submit(basket, "place-order")
            zero_id = int(receipt_zero.rsplit("/", 1)[1])
            zero_original = receipt_snapshot(zero_id)
            assert zero_original[0] == [(500,)]  # 500 g at the new $9.99/kg rate.
            _, page = owner.get("/manager" + receipt_zero)
            zero_lid = line(zero_id, pear)[0]
            zero_stock = stock(pear)
            state = order_state(zero_id)
            _, rejected = owner.submit(page, f"preview-actual-{zero_lid}", {
                "actual": "400", "reason": "Fake reduction without stock handling"}, problem=True)
            assert f"confirm-actual-{zero_lid}" not in rejected.forms and order_state(zero_id) == state
            for actual, disposition, effect, expected_stock in (
                    (400, "restock", "Release 100 g to available stock", zero_stock + 100),
                    (350, "writeoff", "Write off 50 g", zero_stock + 100),
                    (0, "restock", "Release 350 g to available stock", zero_stock + 450)):
                _, page = owner.get("/manager" + receipt_zero)
                before = order_state(zero_id)
                review = preview(page, zero_lid, actual, disposition)
                review.contains(effect)
                assert order_state(zero_id) == before
                confirmation = copy.deepcopy(review.forms[f"confirm-actual-{zero_lid}"])
                _, page = owner.post(confirmation)
                assert stock(pear) == expected_stock
                assert line(zero_id, pear)[1:5] == (500, actual, actual, 1)
                saved = order_state(zero_id)
                owner.post(confirmation)
                assert order_state(zero_id) == saved
                assert receipt_snapshot(zero_id) == zero_original
            page.contains("0 g measured", "100% shopped")
            _, page = owner.submit(page, "advance-order")
            assert query("SELECT status,final_total FROM orders WHERE id=?", (zero_id,)) == [("Ready", 0)]
            final_stock = stock(pear)
            before_restart = (order_state(oid), order_state(zero_id))
            stop()
            process = start()
            assert (order_state(oid), order_state(zero_id)) == before_restart
            _, persisted = owner.get(receipt)
            persisted.contains("FINAL TOTAL", "$5.33", "33% shopped")
            _, page = owner.get("/manager" + receipt_zero)
            page.contains("FINAL TOTAL", "$0.00", "0 g measured")
            _, page = owner.submit(page, "advance-order")
            assert query("SELECT status,final_total FROM orders WHERE id=?", (zero_id,)) == [("Completed", 0)]
            assert stock(pear) == final_stock
            assert receipt_snapshot(oid) == original and receipt_snapshot(zero_id) == zero_original
            assert not query("PRAGMA foreign_key_check")
            print(json.dumps({"result": "passed", "checks": [
                "manager-created gram products and stock through parsed forms",
                "gram bounds, mixed basket hold and checkout replay without double deduction",
                "per-product picker quantity and immutable requested receipt/rate",
                "527 g at 349 cents/kg previews 184 cents without mutation",
                "explicit confirmation, HTML/HTMX parity and exact replay",
                "tampered and stale reviews rejected without mutation",
                "cross-session receipt and measurement privacy",
                "mixed partial completion with product-line progress and final receipt",
                "restock/writeoff allocation reductions and explicit measured zero",
                "frozen zero final, process restart, collection and foreign-key integrity",
                "HTML form/ID/label structure; no browser or visual QA claimed"]}))
        except BaseException:
            if log_path.exists():
                print(log_path.read_text()[-6000:])
            raise
        finally:
            stop()


if __name__ == "__main__":
    main()
