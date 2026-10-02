#!/usr/bin/env python3
"""Real-process order-message smoke against disposable, fictional demo data.

Independent customer/manager, phone and stranger cookie jars use rendered forms.
SQLite is read-only verification only. Credentials and response bodies are never
printed. This is HTTP/process evidence, not browser or physical-device QA.
"""
import copy
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

from handheld_smoke import QuietClient
from weighted_smoke import Page, PASSWORD, ROOT


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, url):
        return None


def raw_request(client, path, fields=None, hx=False, headers=None, discard=False):
    """Keep real actor cookies; expose 303 and optionally discard the response."""
    supplied = {"Origin": client.origin} if fields is not None else {}
    if hx:
        supplied["HX-Request"] = "true"
    supplied.update(headers or {})
    data = None if fields is None else urllib.parse.urlencode(fields).encode()
    request = urllib.request.Request(client.origin + path, data=data, headers=supplied)
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(client.jar), NoRedirect())
    try:
        response = opener.open(request, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        if discard:
            # Simulate a committed send whose response the caller never consumes.
            # Its outcome is established separately through read-only verification.
            return None
        return response.status, response.headers, response.read().decode()


def main():
    binary = Path(os.environ.get("ORDER_MESSAGES_SMOKE_BINARY", ROOT / "bin/shop"))
    assert binary.is_file(), "Build bin/shop before running order-message smoke"
    with tempfile.TemporaryDirectory(prefix="shopper-order-messages-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        db_path = Path(directory) / "fake.db"
        log_path = Path(directory) / "server.log"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=str(db_path), MANAGER_PASSWORD=PASSWORD,
                   DEMO_MODE="true", GOMAXPROCS="2")
        desktop, phone, attacker, replacement = (QuietClient(origin) for _ in range(4))
        process = None
        secrets = []
        private_notes = ["PRIVATE fake receiving hold", "PRIVATE fake packaging note",
                         "PRIVATE fake phone scale report"]

        def query(sql, args=()):
            with sqlite3.connect(db_path.as_uri() + "?mode=ro", uri=True) as db:
                db.execute("PRAGMA query_only=ON")
                return db.execute(sql, args).fetchall()

        def snapshot(messages=True):
            # Full-table equality catches incidental session, basket, inventory,
            # pick/weight-command, assignment, receipt and audit side effects.
            with sqlite3.connect(db_path.as_uri() + "?mode=ro", uri=True) as db:
                db.execute("PRAGMA query_only=ON")
                names = [row[0] for row in db.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
                return {name: sorted(db.execute('SELECT * FROM "' + name + '"').fetchall(), key=repr)
                        for name in names if messages or name != "order_messages"}

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
                    assert proc.poll() is None, "Temporary message server exited during startup"
                    time.sleep(.05)
                raise AssertionError("Temporary message server startup timed out")
            except BaseException:
                proc.terminate()
                proc.wait(timeout=5)
                raise

        def stop():
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=5)

        def pair(client):
            pair_path = f"/manager/orders/{oid}/phone"
            _, page = desktop.get(pair_path)
            _, issued = desktop.post(page.action(pair_path + "/issue"))
            code = issued.controls["phone-pairing-code"]["value"]
            _, connect = client.get("/handheld/")
            secrets.extend([code, connect.forms["handheld-connect-form"]["fields"]["csrf"]])
            client.submit(connect, "handheld-connect-form", {"pairing_code": code})
            assert {cookie.name for cookie in client.jar} == {"shop_handheld"}, "Phone inherited broad identity"
            secrets.extend(cookie.value for cookie in client.jar)

        def manager_lock(page):
            forms = [form for form in page.all_forms if form["action"] == "/manager/logout"]
            assert forms, "Rendered manager lock form missing"
            assert all(form["method"] == forms[0]["method"] and form["fields"] == forms[0]["fields"] for form in forms), "Ambiguous manager lock controls"
            return forms[0]

        def no_private(page):
            assert all(note not in page.html for note in private_notes), "Private manager/report note leaked into chat"
            assert all(secret not in page.html for secret in secrets if secret and secret not in (
                page.forms.get("order-message-form", {}).get("fields", {}).get("csrf"),)), "Authentication credential leaked into chat"

        def conversation(client, path, feed=False):
            status, headers, body = raw_request(client, path, hx=feed)
            assert status == 200, "Message conversation GET failed"
            assert headers.get("Cache-Control") == "no-store", "Conversation became cacheable"
            page = Page(body)
            assert "order-message-feed" in page.ids, "Message feed missing"
            assert ("order-message-composer" in page.ids) is not feed, "Feed response replaced the composer"
            assert all(asset not in body for asset in ("/static/scanner.js", "/static/handheld.js", "/static/app.js")), "Chat loaded picking/controller lifecycle"
            no_private(page)
            return headers, page

        def form_for(page, body=None):
            assert "order-message-form" in page.forms, "Message form missing"
            form = copy.deepcopy(page.forms["order-message-form"])
            assert {"csrf", "conversation_key", "assignment_id", "assignment_version", "command_key", "body"} <= set(form["fields"]), "Message context fields missing"
            secrets.append(form["fields"]["csrf"])
            if body is not None:
                form["fields"]["body"] = body
            return form

        def send(client, form, result="sent", hx=True):
            status, headers, body = raw_request(client, form["action"], form["fields"], hx=hx)
            if not hx and result == "sent":
                assert status == 303 and headers.get("Location") == form["action"], "Plain HTML send did not redirect safely"
                return None
            assert status == 200 and headers.get("X-Messages-Result") == result, "Unexpected message send outcome"
            if result == "sent":
                assert headers.get("X-Messages-Key") == form["fields"]["command_key"], "Send acknowledged a different request"
            page = Page(body)
            assert "order-message-composer" in page.ids, "Send response omitted composer"
            assert ("order-message-feed" not in page.ids) is hx, "Enhanced send replaced conversation shell/feed"
            no_private(page)
            return page

        def check(client, form, result, hx=True):
            before = snapshot()
            status, headers, body = raw_request(client, form["action"] + "/check", form["fields"], hx=hx)
            assert status == 200 and headers.get("X-Messages-Result") == result, "Ambiguous-result lookup returned wrong outcome"
            assert snapshot() == before, "Read-only message lookup mutated application state"
            if result == "sent":
                assert headers.get("X-Messages-Key") == form["fields"]["command_key"], "Lookup acknowledged the wrong request"
            page = Page(body)
            assert ("order-message-feed" not in page.ids) is hx, "Lookup returned the wrong HTML scope"
            no_private(page)
            return page

        def denied(client, path, fields=None):
            before = snapshot()
            status, headers, body = raw_request(client, path, fields, hx=True)
            assert status == 403 and headers.get("X-Messages-Access") == "ended", "Unauthorized actor retained conversation access"
            assert not any(headers.get(name) for name in (
                "X-Messages-Revision", "X-Messages-Key", "X-Messages-Context", "X-Messages-Result")), "Denied response leaked transcript metadata"
            assert reference not in body and "order-message-feed" not in body, "Denied response leaked order or transcript"
            assert snapshot() == before, "Denied chat request mutated application state"

        try:
            process = start()
            assert query("SELECT max(version) FROM schema_version") == [(17,)], "Unexpected message schema"
            desktop.login()
            _, create = desktop.get("/manager/catalog?tab=new")
            product_form = create.forms["create-product"]
            category = next(value for value in product_form["choices"]["category_id"] if value)
            desktop.post(product_form, {"name": "Message smoke loose pears", "sku": "MESSAGE-WEIGHT-PEAR",
                "description": "Disposable fictional messaging test", "category_id": category,
                "type_id": "0", "icon": "apple", "sale_unit": "g", "price": "349", "quantity_step": "50"})
            pid = query("SELECT id FROM products WHERE sku='MESSAGE-WEIGHT-PEAR'")[0][0]
            _, stock = desktop.get(f"/manager/stock?product={pid}")
            desktop.submit(stock, f"save-stock-{pid}", {"delta": "10000", "reason": "Fake message test stock"})
            _, product = desktop.get(f"/products/{pid}")
            desktop.submit(product, f"detail-add-{pid}", {"quantity": "500", "return": "cart"})
            _, home = desktop.get("/")
            _, cart = desktop.submit(home, "add-1", {"quantity": "2", "return": "cart"})
            receipt_path, _ = desktop.submit(cart, "place-order", {"instructions": "Fake message smoke only"})
            oid = int(receipt_path.rsplit("/", 1)[1])
            customer_path = receipt_path + "/messages"
            phone_path = "/handheld/messages"
            reference = query("SELECT reference FROM orders WHERE id=?", (oid,))[0][0]
            receipt = query("SELECT * FROM order_items WHERE order_id=? ORDER BY product_id", (oid,))
            original_total = query("SELECT total FROM orders WHERE id=?", (oid,))
            _, assignment = desktop.get(f"/manager/shoppers?order={oid}")
            desktop.submit(assignment, "save-shopper-task", {"shopper_id": "1", "reason": "Fake message test assignment"})
            _, ticket = desktop.get(f"/manager/orders/{oid}")
            _, ticket = desktop.submit(ticket, "place-order-hold", {"reason": private_notes[0]})
            desktop.submit(ticket, "add-order-note", {"reason": private_notes[1]})
            pair(phone)
            secrets.extend(cookie.value for cookie in desktop.jar)

            # Keep a genuine phone report and pending weight review alongside
            # chat. Neither private operational text nor mutation authority may
            # cross into a message, and chat must not stale the pending approval.
            lid = query("SELECT id FROM working_order_items WHERE order_id=? AND product_id=?", (oid, pid))[0][0]
            code = query("SELECT normalized_value FROM product_codes WHERE product_id=? AND scheme='demo_local' AND archived=0", (pid,))[0][0]
            _, item = phone.get(f"/handheld/?line={lid}")
            phone.submit(item, "handheld-report-form", {"kind": "weight_stock", "note": private_notes[2]})
            private_audit = " ".join(reason + " " + details for reason, details in query(
                "SELECT reason,details FROM order_events WHERE order_id=?", (oid,)))
            assert all(note in private_audit for note in private_notes), "Private-note isolation fixture was not saved"
            _, item = phone.get(f"/handheld/?line={lid}")
            _, recognized = phone.submit(item, "handheld-scan-form", {"code": code}, hx=True)
            _, preview = phone.submit(recognized, "handheld-weight-preview-form", {"actual": "527", "note": "Fake pending scale review"}, hx=True)
            pending_weight = copy.deepcopy(preview.forms["handheld-weight-confirm-form"])
            _, home = desktop.get("/")
            desktop.submit(home, "add-2", {"quantity": "1", "return": "cart"})
            assert query("SELECT count(*) FROM cart")[0][0] > 0, "Live basket invariant fixture missing"
            baseline = snapshot(messages=False)
            before = snapshot()
            _, customer = conversation(desktop, customer_path)
            _, worker = conversation(phone, phone_path)
            conversation(phone, phone_path + "?feed=1", feed=True)
            assert snapshot() == before, "Conversation reads changed application state"
            assert {cookie.name for cookie in phone.jar} == {"shop_handheld"}, "Chat minted broad phone authority"

            # Ownership is checked before malformed cursor handling, for an
            # anonymous visitor and a foreign shared-demo manager alike.
            rejected_form = form_for(customer, "Fake unauthorized note")
            for manager in (False, True):
                if manager:
                    attacker.login()
                for path in (customer_path, customer_path + "?feed=1&after=bad&limit=-2", phone_path):
                    denied(attacker, path)
                denied(attacker, customer_path, rejected_form["fields"])
                denied(attacker, customer_path + "/check", rejected_form["fields"])
            denied(phone, customer_path)
            # A foreign manager login is a genuine independent mutation, so the
            # message-only invariant starts again after that setup step.
            baseline = snapshot(messages=False)

            first_text = "A fake note <script>alert(1)</script>\nSecond line"
            first = form_for(customer, "  " + first_text.replace("\n", "\r\n") + "  ")
            send(desktop, first)
            assert query("SELECT actor,sender_name,body FROM order_messages WHERE order_id=? ORDER BY revision", (oid,)) == [("customer", "Demo customer", first_text)], "Customer attribution or normalized text changed"
            once = snapshot()
            send(desktop, first)
            check(desktop, first, "sent", hx=False)
            assert snapshot() == once, "Exact retry or lookup duplicated a saved note"
            _, feed = conversation(phone, phone_path + "?feed=1", feed=True)
            assert "&lt;script&gt;alert(1)&lt;/script&gt;" in feed.html and "<script>alert(1)" not in feed.html, "Plaintext note became executable HTML"
            assert first_text.splitlines()[0] in feed.text, "Customer note did not reach independent phone"

            reply_text = "Fake shopper reply: pears are on the demo shelf"
            reply = form_for(worker, reply_text)
            send(phone, reply, hx=False)
            _, customer = conversation(desktop, customer_path)
            assert reply_text in customer.text and "Avery Morgan" in customer.text, "Phone reply or immutable attribution missing"
            _, after = conversation(desktop, customer_path + "?feed=1&after=1", feed=True)
            _, older = conversation(phone, phone_path + "?feed=1&before=2", feed=True)
            assert "order-message-2" in after.ids and "order-message-1" not in after.ids, "After cursor returned old rows"
            assert "order-message-1" in older.ids and "order-message-2" not in older.ids, "Before cursor returned newer rows"
            for query_string in ("after=bad", "before=-1", "before=2&after=1", "limit=51"):
                assert raw_request(desktop, customer_path + "?" + query_string)[0] == 400, "Invalid authorized cursor accepted"

            # An absent read-only check retains the exact key and draft and does
            # not claim that the uncertain earlier request can never complete.
            _, worker = conversation(phone, phone_path)
            unknown = form_for(worker, "Fake uncertain request never submitted")
            absent = check(phone, unknown, "absent", hx=False)
            retry = form_for(absent)
            assert retry["fields"] == unknown["fields"], "Absent lookup changed the exact retry payload"
            assert absent.controls["order-message-composer"]["data-messages-send-state"] == "unconfirmed", "Absent lookup presented a confirmed outcome"
            assert "readonly" in absent.controls["order-message-body"], "Absent lookup permitted editing uncertain payload"
            check(phone, retry, "absent")
            send(phone, retry)
            check(phone, retry, "sent")

            changed = copy.deepcopy(first)
            changed["fields"]["body"] = "A deliberately changed fake draft <review>"
            before = snapshot()
            recovered = send(desktop, changed, "stale")
            reviewed = form_for(recovered)
            assert reviewed["fields"]["body"] == changed["fields"]["body"], "Changed-key conflict discarded draft"
            assert reviewed["fields"]["command_key"] != first["fields"]["command_key"], "Changed payload reused consumed key"
            checked = check(desktop, changed, "stale", hx=False)
            assert form_for(checked)["fields"]["body"] == changed["fields"]["body"], "Changed-key lookup cleared an unsent draft"
            assert snapshot() == before, "Changed-key recovery sent without deliberate confirmation"
            send(desktop, reviewed)

            # Unicode limits apply to code points and bytes after decoding; the
            # valid 2,000-byte maximum still fits an encoded HTML form request.
            _, customer = conversation(desktop, customer_path)
            maximum = form_for(customer, "🍐" * 500)
            send(desktop, maximum, hx=False)
            assert query("SELECT length(body),length(CAST(body AS BLOB)) FROM order_messages WHERE order_id=? AND command_key=?", (oid, maximum["fields"]["command_key"])) == [(500, 2000)], "Maximum valid Unicode note was not preserved"
            _, customer = conversation(desktop, customer_path)
            over = form_for(customer, "🍐" * 501)
            before = snapshot()
            invalid = send(desktop, over, "invalid", hx=False)
            assert form_for(invalid)["fields"]["body"] == over["fields"]["body"], "Oversized Unicode draft was silently truncated to a valid send"
            huge = copy.deepcopy(over)
            huge["fields"]["body"] = "🍐" * 1000
            status, headers, _ = raw_request(desktop, customer_path, huge["fields"], hx=True)
            assert status == 400 and headers.get("X-Messages-Result") == "invalid", "Oversized encoded request accepted"
            control = copy.deepcopy(over)
            control["fields"]["body"] = "Fake\x00note"
            send(desktop, control, "invalid")
            assert snapshot() == before, "Invalid text created a message or command"

            for client, form in ((desktop, first), (phone, reply)):
                for suffix in ("", "/check"):
                    bad_csrf = dict(form["fields"], csrf="invalid-smoke-csrf")
                    if client is desktop:
                        before = snapshot()
                        status, headers, recovered_body = raw_request(client, form["action"] + suffix, bad_csrf, hx=True)
                        assert status == 200 and headers.get("X-Messages-Result") == "sent", "Same-owner CSRF recovery failed to reconcile earlier saved intent"
                        assert snapshot() == before, "CSRF recovery mutated data"
                        no_private(Page(recovered_body))
                    else:
                        denied(client, form["action"] + suffix, bad_csrf)
                    for bad_headers in ({"Origin": "https://invalid.example"},
                                        {"Origin": ""}, {"Sec-Fetch-Site": "cross-site"}):
                        before = snapshot()
                        status, _, _ = raw_request(client, form["action"] + suffix, form["fields"], headers=bad_headers)
                        assert status == 403 and snapshot() == before, "Cross-origin message/check bypassed safeguards"
            assert snapshot(messages=False) == baseline, "Message activity changed basket, receipt, stock, task or pending picking state"

            # Lose a committed phone response, restart this exact temporary
            # process, then retry the original form without a new key or cookie.
            _, worker = conversation(phone, phone_path)
            lost = form_for(worker, "Fake committed message with an unseen response")
            before_count = query("SELECT count(*) FROM order_messages")[0][0]
            raw_request(phone, lost["action"], lost["fields"], hx=True, discard=True)
            assert query("SELECT count(*) FROM order_messages")[0][0] == before_count + 1, "Lost-response fixture did not commit"
            committed = snapshot()
            phone_identity = tuple((cookie.name, cookie.value) for cookie in phone.jar)
            stop()
            process = start()
            assert snapshot() == committed, "Restart changed committed messages or application data"
            check(phone, lost, "sent")
            send(phone, lost)
            assert snapshot() == committed, "Restart exact retry duplicated the committed message"
            assert tuple((cookie.name, cookie.value) for cookie in phone.jar) == phone_identity, "Restart replaced the phone identity"
            assert snapshot(messages=False) == baseline, "Messages invalidated pending weight review"
            _, weighed = phone.post(pending_weight, hx=True)
            assert "Saved 527 g" in weighed.text, "Chat invalidated a previously reviewed phone weight approval"
            assert query("SELECT allocated_quantity,picked_quantity,measurement_confirmed FROM working_order_items WHERE id=?", (lid,)) == [(527, 527, 1)], "Pending measurement did not remain confirmable"

            # Manager sign-out/sign-in rotates this customer's CSRF token but
            # leaves the owner and conversation intact. Retain an unsent form,
            # refresh it read-only, then send only after a deliberate review.
            _, customer = conversation(desktop, customer_path)
            old_form = form_for(customer, "Fake draft retained through manager sign-in")
            _, ticket = desktop.get(f"/manager/orders/{oid}")
            desktop.post(manager_lock(ticket))
            desktop.login()
            after_rotation = snapshot()
            recovered = send(desktop, old_form, "absent")
            fresh_form = form_for(recovered)
            assert fresh_form["fields"]["body"] == old_form["fields"]["body"], "Manager sign-in erased customer draft"
            assert fresh_form["fields"]["csrf"] != old_form["fields"]["csrf"], "Manager sign-in recovery retained expired CSRF"
            assert fresh_form["fields"]["command_key"] == old_form["fields"]["command_key"], "Token refresh forked a second key for the same intent"
            check(desktop, old_form, "absent")
            assert snapshot() == after_rotation, "Form recovery sent a message automatically"
            send(desktop, fresh_form)
            # If the original outcome was already saved, rotating CSRF cannot
            # cause the same retained key to become a duplicate new message.
            _, ticket = desktop.get(f"/manager/orders/{oid}")
            desktop.post(manager_lock(ticket))
            desktop.login()
            after_second_rotation = snapshot()
            send(desktop, fresh_form)
            check(desktop, fresh_form, "sent")
            assert snapshot() == after_second_rotation, "Rotated-token saved-intent recovery duplicated a message"

            # Reassignment must close old authority before replay lookup, retain
            # old attribution, and demand review of a customer's old draft.
            _, customer = conversation(desktop, customer_path)
            stale_assignment = form_for(customer, "Fake draft retained across shopper reassignment")
            history = query("SELECT * FROM order_messages WHERE order_id=? ORDER BY revision", (oid,))
            _, assignment = desktop.get(f"/manager/shoppers?order={oid}")
            desktop.submit(assignment, "save-shopper-task", {"action": "reassign", "shopper_id": "2", "reason": "Fake message task handover"})
            for suffix, fields in (("", None), ("", lost["fields"]), ("/check", lost["fields"])):
                denied(phone, phone_path + suffix, fields)
            pair(replacement)
            _, current_worker = conversation(replacement, phone_path)
            assert "Avery Morgan" in current_worker.text and "Jordan Lee" in current_worker.text and reply_text in current_worker.text, "New grant lost prior attribution/history"
            assert query("SELECT * FROM order_messages WHERE order_id=? ORDER BY revision", (oid,)) == history, "Reassignment rewrote old messages"
            before = snapshot()
            stale = send(desktop, stale_assignment, "stale", hx=False)
            recovered_assignment = form_for(stale)
            assert recovered_assignment["fields"]["body"] == stale_assignment["fields"]["body"], "Reassignment discarded customer draft"
            assert recovered_assignment["fields"]["assignment_version"] != stale_assignment["fields"]["assignment_version"], "Reassignment did not refresh recipient context"
            assert recovered_assignment["fields"]["command_key"] != stale_assignment["fields"]["command_key"], "Reassignment recovery retained obsolete command key"
            check(desktop, stale_assignment, "stale")
            assert snapshot() == before, "Stale assignment sent before review"
            baseline = snapshot(messages=False)
            send(desktop, recovered_assignment)
            new_reply = form_for(current_worker, "Fake reply from the replacement shopper")
            send(replacement, new_reply)
            assert query("SELECT actor,sender_name,shopper_id FROM order_messages WHERE order_id=? ORDER BY revision DESC LIMIT 1", (oid,)) == [("worker", "Jordan Lee", 2)], "New message used old shopper attribution"
            assert snapshot(messages=False) == baseline, "Reassigned chat mutated fulfillment"

            # Ready ends phone authority, while the customer keeps an immutable
            # read-only history. Genuine rendered manager forms close the order.
            counted = query("SELECT id FROM working_order_items WHERE order_id=? AND product_id=1", (oid,))[0][0]
            _, ticket = desktop.get(f"/manager/orders/{oid}")
            _, ticket = desktop.submit(ticket, f"mark-picked-{counted}")
            _, ticket = desktop.submit(ticket, "release-order-hold", {"reason": "Fake manager resolved receiving review"})
            desktop.submit(ticket, "advance-order")
            assert query("SELECT status FROM orders WHERE id=?", (oid,)) == [("Ready",)], "Order did not reach Ready"
            ready = snapshot()
            headers, closed = conversation(desktop, customer_path)
            assert headers.get("X-Messages-State") == "closed", "Ready customer conversation stayed writable"
            assert reply_text in closed.text and new_reply["fields"]["body"] in closed.text, "Ready removed customer history"
            assert "disabled" in closed.controls["message-send"], "Closed history offered sending"
            send(desktop, recovered_assignment, "invalid")
            check(desktop, recovered_assignment, "sent", hx=False)
            for suffix, fields in (("", None), ("", new_reply["fields"]), ("/check", new_reply["fields"])):
                denied(replacement, phone_path + suffix, fields)
            assert snapshot() == ready, "Closed conversation/retry mutated history or fulfillment"
            assert query("SELECT * FROM order_items WHERE order_id=? ORDER BY product_id", (oid,)) == receipt, "Messaging workflow changed immutable receipt"
            assert query("SELECT total FROM orders WHERE id=?", (oid,)) == original_total, "Messaging workflow changed placed total"
            assert query("PRAGMA foreign_key_check") == [], "Message workflow broke foreign keys"
            stop()
            logs = log_path.read_text()
            assert all(secret not in logs for secret in secrets if secret), "Authentication secret appeared in application logs"
            print("Order-message process smoke passed: independent customer/phone/stranger sessions; two-way escaped plaintext; plain HTML and scoped HX; cursors; exact retries and read-only uncertain checks; Unicode/CSRF/origin guards; unchanged basket/stock/receipt/picking state; pending weight confirmation; lost-response restart; manager sign-in draft/result recovery; reassignment and immutable attribution; Ready access closure. No browser or physical-device claim.")
        finally:
            stop()


if __name__ == "__main__":
    main()
