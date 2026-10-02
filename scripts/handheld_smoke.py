#!/usr/bin/env python3
"""Real-process phone pairing/picking smoke against disposable fake data.

Three independent cookie jars represent desktop, phone and unrelated visitor.
All mutations use rendered HTTP forms; SQLite is read-only verification only.
Pairing codes, worker tokens, CSRF values and response bodies are never printed.
This is process/HTTP evidence, not camera or physical-device/browser QA.
"""
import copy
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.request

from weighted_smoke import Client, Page, PASSWORD, ROOT


class QuietClient(Client):
    """Keep credential-bearing form response bodies out of assertion output."""
    def get(self, path, hx=False):
        status, _, final, body = self.request(path, hx=hx)
        assert status == 200, "GET did not succeed"
        return final, Page(body)

    def post(self, form, changes=None, hx=False, problem=False):
        fields = dict(form["fields"], **(changes or {}))
        for name, options in form["choices"].items():
            assert name not in fields or str(fields[name]) in options, "Unrendered form choice"
        assert form["method"].lower() == "post", "Expected a POST form"
        status, _, final, body = self.request(form["action"], fields, hx)
        assert status == 200, "POST did not succeed"
        page = Page(body)
        assert page.problem == problem, "Unexpected manager validation result"
        return final, page


def main():
    binary = ROOT / "bin/shop"
    assert binary.is_file(), "Build bin/shop before running handheld smoke"
    with tempfile.TemporaryDirectory(prefix="shopper-handheld-smoke-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        path = Path(directory) / "fake.db"
        log_path = Path(directory) / "server.log"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=str(path), MANAGER_PASSWORD=PASSWORD,
                   DEMO_MODE="true", GOMAXPROCS="2")
        desktop, phone, attacker = (QuietClient(origin) for _ in range(3))
        process = None
        secrets = []

        def query(statement, args=()):
            with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as db:
                db.execute("PRAGMA query_only=ON")
                return db.execute(statement, args).fetchall()

        def start():
            with log_path.open("ab") as log:
                proc = subprocess.Popen([str(binary)], env=env, stdout=log, stderr=log)
            try:
                for _ in range(100):
                    try:
                        if desktop.request("/healthz")[0] == 200:
                            return proc
                    except OSError:
                        pass
                    assert proc.poll() is None, "Temporary server exited during startup"
                    time.sleep(.05)
                raise AssertionError("Temporary server startup timed out")
            except BaseException:
                proc.terminate()
                proc.wait(timeout=5)
                raise

        def stop():
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=5)

        def cookie_names(client):
            return {cookie.name for cookie in client.jar}

        def state_snapshot(oid):
            return (
                query("SELECT * FROM orders WHERE id=?", (oid,)),
                query("SELECT * FROM working_order_items WHERE order_id=? ORDER BY id", (oid,)),
                query("SELECT * FROM order_events WHERE order_id=? ORDER BY id", (oid,)),
                query("SELECT * FROM handheld_pick_commands ORDER BY grant_id,command_key"),
                query("SELECT * FROM order_event_actors ORDER BY event_id"),
                query("SELECT id,stock,version FROM products ORDER BY id"),
            )

        try:
            process = start()
            assert query("SELECT max(version) FROM schema_version") == [(16,)], "Unexpected schema"
            _, home = desktop.get("/")
            desktop.submit(home, "add-1", {"quantity": "2", "return": "cart"})
            _, home = desktop.get("/")
            _, cart = desktop.submit(home, "add-2", {"quantity": "1", "return": "cart"})
            receipt_url, _ = desktop.submit(cart, "place-order", {
                "instructions": "Fictional shopper smoke instructions only"
            })
            oid = int(receipt_url.rsplit("/", 1)[1])
            receipt = query("SELECT * FROM order_items WHERE order_id=? ORDER BY product_id", (oid,))
            total = query("SELECT total FROM orders WHERE id=?", (oid,))
            stock = query("SELECT id,stock,version FROM products ORDER BY id")
            desktop.login()
            _, assignment = desktop.get(f"/manager/shoppers?order={oid}")
            desktop.submit(assignment, "save-shopper-task", {
                "shopper_id": "1", "reason": "Assign the fictional phone smoke task"
            })
            _, ticket = desktop.get(f"/manager/orders/{oid}")
            private_hold = "Handheld smoke PRIVATE manager receiving hold"
            private_note = "Handheld smoke PRIVATE internal packaging note"
            _, ticket = desktop.submit(ticket, "place-order-hold", {"reason": private_hold})
            desktop.submit(ticket, "add-order-note", {"reason": private_note})
            before_pairing = state_snapshot(oid)

            pairing_path = f"/manager/orders/{oid}/phone"
            _, pairing = desktop.get(pairing_path)
            _, issued = desktop.post(pairing.action(pairing_path + "/issue"))
            pairing_code = issued.controls["phone-pairing-code"]["value"]
            secrets.append(pairing_code)
            assert len(pairing_code.replace("-", "")) == 26, "Invalid pairing code shape"
            assert "data:image/png;base64," in issued.html, "Pairing QR missing"
            assert "/handheld/#pair=" in issued.html, "Pairing URL must use a fragment"
            assert "/handheld/?pair=" not in issued.html, "Pairing secret appeared in a URL query"
            _, refreshed = desktop.get(pairing_path)
            assert pairing_code not in refreshed.html, "One-time code redisplayed after refresh"
            assert state_snapshot(oid) == before_pairing, "Pairing changed order, stock or picking"
            assert query("SELECT status FROM orders WHERE id=?", (oid,)) == [("Placed",)], "Pairing started picking"

            _, connect = phone.get("/handheld/")
            assert cookie_names(phone) == {"shop_handheld_pair"}, "Phone got a customer cookie before connecting"
            _, task = phone.submit(connect, "handheld-connect-form", {"pairing_code": pairing_code})
            assert cookie_names(phone) == {"shop_handheld"}, "Pairing transferred a customer or manager cookie"
            grant_cookie = next(iter(phone.jar))
            secrets.extend([grant_cookie.value, connect.forms["handheld-connect-form"]["fields"]["csrf"]])
            assert grant_cookie.path == "/handheld/", "Worker cookie scope is too broad"
            assert grant_cookie.has_nonstandard_attr("HttpOnly"), "Worker cookie is readable by scripts"
            assert grant_cookie.get_nonstandard_attr("SameSite") == "Strict", "Worker cookie SameSite scope changed"
            assert cookie_names(desktop) == {"shop_session"}, "Desktop identity was replaced or copied"
            owner_cookie = next(iter(desktop.jar)).value
            assert owner_cookie != grant_cookie.value, "Phone reused the customer identity"
            secrets.append(owner_cookie)
            reference = query("SELECT reference FROM orders WHERE id=?", (oid,))[0][0]
            assert reference in task.text and "Avery Morgan" in task.text, "Phone missed assigned task"
            assert private_hold not in task.html and private_note not in task.html, "Private manager notes leaked"
            assert "Under manager review" in task.text, "Safe hold label missing"
            assert state_snapshot(oid) == before_pairing, "Redemption started picking or changed stock"

            # An unrelated visitor can neither reuse a consumed code nor read
            # the assigned customer/manager order, even with the shared demo gate.
            _, outsider_connect = attacker.get("/handheld/")
            _, rejected = attacker.submit(outsider_connect, "handheld-connect-form", {"pairing_code": pairing_code})
            assert reference not in rejected.html and "unavailable or expired" in rejected.text, "Invitation reused"
            assert "shop_handheld" not in cookie_names(attacker), "Attacker received worker authority"
            attacker.login()
            for route in [f"/orders/{oid}", f"/manager/orders/{oid}", pairing_path]:
                assert attacker.request(route)[0] == 404, "Unrelated visitor read assigned order"

            line_id = query("SELECT id FROM working_order_items WHERE order_id=? AND product_id=1", (oid,))[0][0]
            code = query("SELECT normalized_value FROM product_codes WHERE product_id=1 AND scheme='demo_local' AND archived=0")[0][0]
            wrong_code = query("SELECT normalized_value FROM product_codes WHERE product_id=2 AND scheme='demo_local' AND archived=0")[0][0]
            _, product = desktop.get("/products/1")
            assert code in product.html and "/products/1/barcode.png" in product.html, "Rendered product label missing"
            with desktop.opener.open(origin + "/products/1/barcode.png", timeout=5) as response:
                assert response.status == 200 and response.read().startswith(b"\x89PNG\r\n\x1a\n"), "Real label PNG missing"
            _, item = phone.get(f"/handheld/?line={line_id}")
            scan_form = copy.deepcopy(item.forms["handheld-scan-form"])
            before_scan = state_snapshot(oid)
            _, wrong = phone.post(scan_form, {"code": wrong_code})
            assert "another item in this task" in wrong.text, "Wrong item recovery missing"
            assert "handheld-confirm-form" not in wrong.forms, "Wrong label offered confirmation"
            assert state_snapshot(oid) == before_scan, "Wrong label mutated fulfillment"
            _, unknown = phone.post(scan_form, {"code": "SHOPDEMO-999999"})
            assert "not a supported demo-local label" in unknown.text, "Unknown label recovery missing"
            assert state_snapshot(oid) == before_scan, "Unknown label mutated fulfillment"
            _, review = phone.post(scan_form, {"code": code, "source": "manual"})
            pick_form = copy.deepcopy(review.forms["handheld-confirm-form"])
            assert pick_form["fields"]["picked"] == "1", "First scan suggestion should be one explicit unit"
            assert state_snapshot(oid) == before_scan, "Recognition saved a pick without confirmation"
            secrets.append(pick_form["fields"]["csrf"])
            _, saved = phone.post(pick_form, {"picked": "1"})
            assert "Saved an absolute picked count of 1" in saved.text, "Phone confirmation did not report persistence"
            assert query("SELECT status FROM orders WHERE id=?", (oid,)) == [("Picking",)], "First pick did not start Picking"
            assert query("SELECT picked_quantity FROM working_order_items WHERE id=?", (line_id,)) == [(1,)], "Wrong saved absolute count"
            once = state_snapshot(oid)
            _, replay = phone.post(pick_form, {"picked": "1"})
            assert "already saved" in replay.text and state_snapshot(oid) == once, "Retry duplicated a pick or event"
            assert query("SELECT actor,source FROM order_event_actors ORDER BY event_id") == [("worker", "manual")], "Worker source attribution missing"
            _, tracker = desktop.get(receipt_url)
            assert "Picking" in tracker.text, "Desktop tracker missed phone progress"
            assert private_hold not in tracker.html and private_note not in tracker.html, "Private notes leaked to customer"
            assert query("SELECT * FROM order_items WHERE order_id=? ORDER BY product_id", (oid,)) == receipt, "Original receipt changed"
            assert query("SELECT total FROM orders WHERE id=?", (oid,)) == total, "Original total changed"
            assert query("SELECT id,stock,version FROM products ORDER BY id") == stock, "Counted pick deducted stock"
            for route in ["/cart", "/checkout", f"/manager/orders/{oid}/advance", pairing_path + "/issue"]:
                assert phone.request(route, {"csrf": pick_form["fields"]["csrf"]})[0] == 403, "Phone gained customer/manager mutation access"
            assert cookie_names(phone) == {"shop_handheld"}, "Denied phone writes minted customer identity"

            # Restart the actual process at the same origin. No cookie is copied
            # between actors and no lost-response retry receives a fresh key.
            stop()
            process = start()
            _, resumed = phone.get(f"/handheld/?line={line_id}")
            assert reference in resumed.text and "1 picked" in resumed.text, "Restart lost authorized phone progress"
            assert next(iter(phone.jar)).value == grant_cookie.value, "Restart replaced the phone identity"
            assert state_snapshot(oid) == once, "Restart changed persisted task/audit"
            _, replay = phone.post(pick_form, {"picked": "1"})
            assert "already saved" in replay.text and state_snapshot(oid) == once, "Restart retry duplicated command"
            # A retained old confirmation edited after an ambiguous response
            # first conflicts. Explicit re-review creates a new logical command,
            # preserving zero exactly and never writing during recognition.
            _, conflict = phone.post(pick_form, {"picked": "0"})
            continuation = copy.deepcopy(conflict.forms["handheld-scan-form"])
            assert continuation["fields"]["picked"] == "0", "Edited recovery count was lost"
            assert continuation["fields"]["command_key"] == pick_form["fields"]["command_key"], "Original retry key was replaced too early"
            assert state_snapshot(oid) == once, "Changed old payload mutated fulfillment"
            _, rereview = phone.post(continuation)
            corrected = copy.deepcopy(rereview.forms["handheld-confirm-form"])
            assert corrected["fields"]["picked"] == "0", "Explicit re-review changed zero draft"
            assert corrected["fields"]["command_key"] != pick_form["fields"]["command_key"], "Explicit re-review retained a consumed key"
            assert state_snapshot(oid) == once, "Explicit re-review automatically saved a correction"
            phone.post(corrected)
            zero_state = state_snapshot(oid)
            assert query("SELECT picked_quantity FROM working_order_items WHERE id=?", (line_id,)) == [(0,)], "Recovered zero count did not save"
            assert query("SELECT count(*) FROM handheld_pick_commands") == [(2,)], "Recovery created duplicate commands"
            _, original_replay = phone.post(pick_form, {"picked": "1"})
            _, correction_replay = phone.post(corrected)
            assert "already saved with a picked count of 1" in original_replay.text, "Original retry outcome changed"
            assert "already saved with a picked count of 0" in correction_replay.text, "Correction retry outcome changed"
            assert state_snapshot(oid) == zero_state, "Retry of either intent overwrote corrected progress"
            _, resumed = phone.get(f"/handheld/?line={line_id}")
            _, next_review = phone.submit(resumed, "handheld-scan-form", {"code": code})
            next_pick = copy.deepcopy(next_review.forms["handheld-confirm-form"])
            phone.post(next_pick, {"picked": "2"})
            assert query("SELECT picked_quantity FROM working_order_items WHERE id=?", (line_id,)) == [(2,)], "Resume did not save absolute count"
            assert query("SELECT * FROM order_items WHERE order_id=? ORDER BY product_id", (oid,)) == receipt, "Resumed picking changed receipt"
            assert query("SELECT id,stock,version FROM products ORDER BY id") == stock, "Resumed picking changed stock"

            _, pairing = desktop.get(pairing_path)
            desktop.post(pairing.action(pairing_path + "/revoke"))
            revoked_snapshot = state_snapshot(oid)
            status, headers, _, denied_body = phone.request(next_pick["action"], dict(next_pick["fields"], picked="2"))
            assert status == 200 and headers.get("X-Handheld-Error") == "revoked", "Revoked request lacked safe disconnect response"
            assert reference not in denied_body and "disconnected" in denied_body, "Revoked task projection leaked"
            assert state_snapshot(oid) == revoked_snapshot, "Exact old retry bypassed revocation"
            assert "shop_handheld" not in cookie_names(phone) and "shop_session" not in cookie_names(phone), "Revocation retained broad browser authority"
            assert query("PRAGMA foreign_key_check") == [], "Foreign key integrity failure"
            stop()
            log = log_path.read_text()
            assert all(secret not in log for secret in secrets if secret), "Application log contained an authentication secret"
            print("Handheld process smoke passed: independent desktop/phone/attacker cookies; one-use pairing; wrong/correct labels; explicit absolute count and replay; private-note isolation; stock/receipt preservation; restart and edited lost-response recovery; revocation before replay. No physical-camera claim.")
        finally:
            stop()


if __name__ == "__main__":
    main()
