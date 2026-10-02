#!/usr/bin/env python3
"""Real-process phone weights/reports; disposable local data, no browser claim."""
import copy
import os
from pathlib import Path
import re
import socket
import sqlite3
import subprocess
import tempfile
import time

from handheld_smoke import QuietClient
from weighted_smoke import PASSWORD, ROOT


def main():
    with tempfile.TemporaryDirectory(prefix="shopper-phone-weight-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        path = Path(directory) / "fake.db"
        log_path = Path(directory) / "server.log"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=str(path), MANAGER_PASSWORD=PASSWORD, DEMO_MODE="true", GOMAXPROCS="2")
        desktop, phone, attacker = (QuietClient(origin) for _ in range(3))
        process = None
        secrets = []

        def query(sql, args=()):
            with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as db:
                db.execute("PRAGMA query_only=ON")
                return db.execute(sql, args).fetchall()

        def start():
            with log_path.open("ab") as log:
                proc = subprocess.Popen([str(ROOT / "bin/shop")], env=env, stdout=log, stderr=log)
            for _ in range(100):
                try:
                    if desktop.request("/healthz")[0] == 200:
                        return proc
                except OSError:
                    pass
                assert proc.poll() is None, "Temporary app exited"
                time.sleep(.05)
            proc.terminate()
            proc.wait(timeout=5)
            raise AssertionError("Temporary app did not start")

        def stop():
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=5)

        def fulfillment():
            return (query("SELECT * FROM orders WHERE id=?", (oid,)),
                    query("SELECT * FROM working_order_items WHERE order_id=? ORDER BY id", (oid,)),
                    query("SELECT * FROM order_events WHERE order_id=? ORDER BY id", (oid,)),
                    query("SELECT * FROM order_event_actors ORDER BY event_id"),
                    query("SELECT * FROM handheld_pick_commands ORDER BY grant_id,command_key"),
                    query("SELECT id,stock,version FROM products ORDER BY id"))

        def receipt():
            return (query("SELECT total FROM orders WHERE id=?", (oid,)),
                    query("SELECT * FROM order_items WHERE order_id=? ORDER BY product_id", (oid,)))

        def measure_form(actual, disposition="", hx=False):
            _, item = phone.get(f"/handheld/?line={lid}")
            _, recognized = phone.submit(item, "handheld-scan-form", {"code": code}, hx=hx)
            assert "handheld-weight-preview-form" in recognized.forms and "handheld-confirm-form" not in recognized.forms
            _, preview = phone.submit(recognized, "handheld-weight-preview-form", {
                "actual": str(actual), "disposition": disposition, "note": "Fake scale observation"}, hx=hx)
            return preview

        try:
            process = start()
            desktop.login()
            _, create = desktop.get("/manager/catalog?tab=new")
            form = create.forms["create-product"]
            category = next(v for v in form["choices"]["category_id"] if v)
            desktop.post(form, {"name": "Phone smoke loose pears", "sku": "PHONE-WEIGHT-PEAR", "description": "Disposable fake scale example",
                                "category_id": category, "type_id": "0", "icon": "apple", "sale_unit": "g", "price": "349", "quantity_step": "50"})
            pid = query("SELECT id FROM products WHERE sku='PHONE-WEIGHT-PEAR'")[0][0]
            _, stock = desktop.get(f"/manager/stock?product={pid}")
            desktop.submit(stock, f"save-stock-{pid}", {"delta": "10000", "reason": "Fake scale test stock"})
            _, product = desktop.get(f"/products/{pid}")
            code = re.search(r"SHOPDEMO-[A-Z0-9-]+", product.text).group(0)
            desktop.submit(product, f"detail-add-{pid}", {"quantity": "500", "return": "cart"})
            _, home = desktop.get("/")
            _, cart = desktop.submit(home, "add-1", {"quantity": "2", "return": "cart"})
            receipt_path, _ = desktop.submit(cart, "place-order", {"instructions": "Fake phone measurement QA only"})
            oid = int(receipt_path.rsplit("/", 1)[1])
            lid = query("SELECT id FROM working_order_items WHERE order_id=? AND product_id=?", (oid, pid))[0][0]
            original = receipt()
            assert original[0] == [(873,)]
            _, assign = desktop.get(f"/manager/shoppers?order={oid}")
            desktop.submit(assign, "save-shopper-task", {"shopper_id": "1", "reason": "Fake phone weight task"})
            pair_path = f"/manager/orders/{oid}/phone"
            _, pair = desktop.get(pair_path)
            _, issued = desktop.post(pair.action(pair_path + "/issue"))
            pairing = issued.controls["phone-pairing-code"]["value"]
            _, connect = phone.get("/handheld/")
            phone.submit(connect, "handheld-connect-form", {"pairing_code": pairing})
            secrets.extend([pairing, *(cookie.value for client in (desktop, phone) for cookie in client.jar)])
            assert {c.name for c in phone.jar} == {"shop_handheld"}
            before = fulfillment()
            preview = measure_form(527, hx=True)
            for expected in ("527 g", "$1.75", "$1.84", "+$0.09", "Reserve 27 g more", "$8.82"):
                assert expected in preview.text, "Missing weight review consequence"
            assert fulfillment() == before, "Preview mutated fulfillment"
            confirm = copy.deepcopy(preview.forms["handheld-weight-confirm-form"])
            secrets.append(confirm["fields"]["csrf"])
            _, foreign = attacker.post(confirm)
            assert "Phone smoke loose pears" not in foreign.text and fulfillment() == before, "Foreign actor gained task"
            _, saved = phone.post(confirm, hx=True)
            assert "Saved 527 g" in saved.text
            assert query("SELECT allocated_quantity,picked_quantity,measurement_confirmed FROM working_order_items WHERE id=?", (lid,)) == [(527, 527, 1)]
            assert query("SELECT stock FROM products WHERE id=?", (pid,)) == [(9473,)]
            assert receipt() == original
            once = fulfillment()
            phone.post(confirm)
            assert fulfillment() == once, "Exact retry duplicated weight"

            # Explicitly reduced allocations require a physical stock decision.
            invalid = measure_form(480)
            assert "handheld-weight-confirm-form" not in invalid.forms and "480" in invalid.text
            assert fulfillment() == once
            scan = invalid.forms["handheld-scan-form"]
            _, recognized = phone.post(scan)
            _, lower = phone.submit(recognized, "handheld-weight-preview-form", {"disposition": "restock"})
            assert "Release 47 g" in lower.text
            phone.submit(lower, "handheld-weight-confirm-form")
            assert query("SELECT stock FROM products WHERE id=?", (pid,)) == [(9520,)]
            assert receipt() == original

            # A manager change invalidates the displayed approval, keeps drafts,
            # and forces code review plus a fresh explicit weight preview.
            stale_preview = measure_form(490)
            old_confirm = copy.deepcopy(stale_preview.forms["handheld-weight-confirm-form"])
            _, ticket = desktop.get(f"/manager/orders/{oid}")
            private_note = "PRIVATE manager-only measurement context"
            desktop.submit(ticket, "add-order-note", {"reason": private_note})
            _, conflict = phone.post(old_confirm)
            assert "handheld-weight-confirm-form" not in conflict.forms and "490" in conflict.text
            assert private_note not in conflict.html
            _, recognized = phone.submit(conflict, "handheld-scan-form")
            _, new_preview = phone.submit(recognized, "handheld-weight-preview-form")
            new_confirm = new_preview.forms["handheld-weight-confirm-form"]
            assert new_confirm["fields"]["command_key"] != old_confirm["fields"]["command_key"]
            phone.post(new_confirm)
            assert query("SELECT stock FROM products WHERE id=?", (pid,)) == [(9510,)]

            # Restart with separate persistent cookie jars, then record a zero
            # with deliberate write-off rather than inventing resalable stock.
            stop()
            process = start()
            _, resumed = phone.get(f"/handheld/?line={lid}")
            assert "490 g measured" in resumed.text
            zero = measure_form(0, "writeoff")
            _, zero_saved = phone.submit(zero, "handheld-weight-confirm-form")
            assert "Saved 0 g" in zero_saved.text and "Measured zero saved" in zero_saved.text
            assert query("SELECT stock FROM products WHERE id=?", (pid,)) == [(9510,)]
            assert receipt() == original

            # Reports contain no authority to dispose stock or release a hold.
            report = copy.deepcopy(zero_saved.forms["handheld-report-form"])
            stock_before = query("SELECT id,stock,version FROM products ORDER BY id")
            _, reported = phone.post(report, {"kind": "weight_stock", "note": "Fake scale requires manager review"})
            report["fields"].update(kind="weight_stock", note="Fake scale requires manager review")
            assert "Under manager review" in reported.text
            assert query("SELECT id,stock,version FROM products ORDER BY id") == stock_before and receipt() == original
            _, ticket = desktop.get(f"/manager/orders/{oid}")
            assert "Fake scale requires manager review" in ticket.text
            desktop.submit(ticket, "release-order-hold", {"reason": "Fake manager resolved the issue"})
            after_release = fulfillment()
            phone.post(report)
            assert fulfillment() == after_release and query("SELECT attention_reason FROM orders WHERE id=?", (oid,)) == [("",)]
            _, tracker = desktop.get(receipt_path)
            assert "Fake scale requires manager review" not in tracker.html and private_note not in tracker.html
            assert phone.request(f"/manager/orders/{oid}/attention", {"csrf": report["fields"]["csrf"], "action": "release"})[0] == 403

            _, current_pair = desktop.get(pair_path)
            desktop.post(current_pair.action(pair_path + "/revoke"))
            before_denial = fulfillment()
            status, headers, _, denied = phone.request(new_confirm["action"], new_confirm["fields"])
            assert status == 200 and headers.get("X-Handheld-Error") == "revoked" and "Phone smoke loose pears" not in denied
            assert fulfillment() == before_denial, "Revoked exact retry mutated data"
            assert query("PRAGMA foreign_key_check") == []
            stop()
            assert all(value not in log_path.read_text() for value in secrets if value), "Authentication secret in logs"
            print("Phone weight/report smoke passed: independent sessions; real rendered preview/confirm; stock/amount/receipt invariants; stale recovery; restart; measured zero; private manager report/release/replay; revocation. No browser or physical-camera claim.")
        finally:
            stop()


if __name__ == "__main__":
    main()
