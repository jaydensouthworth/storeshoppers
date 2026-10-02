#!/usr/bin/env python3
"""Independent employees exercise a shared store against disposable process data."""
import concurrent.futures
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
from weighted_smoke import Client, Page, ROOT, PASSWORD


def main():
    with tempfile.TemporaryDirectory(prefix="shopper-employee-process-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        dbpath = Path(directory) / "fake.db"
        env = dict(os.environ, APP_ADDR=f"127.0.0.1:{port}", APP_ORIGIN=origin,
                   DATABASE_PATH=str(dbpath), MANAGER_PASSWORD=PASSWORD, DEMO_MODE="true", GOMAXPROCS="2")
        people = [Client(origin), Client(origin)]
        log = open(Path(directory) / "server.log", "ab")
        process = None
        def start():
            proc = subprocess.Popen([str(ROOT / "bin/shop")], env=env, stdout=log, stderr=log)
            for _ in range(100):
                try:
                    if people[0].request("/healthz")[0] == 200:
                        return proc
                except OSError:
                    pass
                assert proc.poll() is None, "Temporary employee server exited"
                time.sleep(.05)
            raise AssertionError("Temporary employee server did not start")
        def get(client, path="/handheld/employee"):
            status, headers, _, body = client.request(path)
            assert status == 200, "Page failed"
            assert headers.get("Referrer-Policy") == "same-origin", "Native form origin policy missing"
            return Page(body)
        def form(page, route):
            found = [x for x in page.all_forms if x["action"] == route]
            assert len(found) == 1, "Expected one rendered form"
            return found[0]
        def post(client, f, changes=None):
            status, _, path, body = client.request(f["action"], dict(f["fields"], **(changes or {})))
            assert status == 200, "Native form failed"
            return path, Page(body)
        def query(sql, params=()):
            with sqlite3.connect(dbpath.as_uri()+"?mode=ro", uri=True) as db:
                db.execute("PRAGMA query_only=ON")
                return db.execute(sql, params).fetchall()
        try:
            process = start()
            pages = [get(c) for c in people]
            assert query("SELECT count(DISTINCT shopper_id) FROM employee_sessions") == [(2,)]
            assert query("SELECT count(*) FROM orders") == [(0,)], "Opening dashboard created orders"
            seeds = [form(p,"/handheld/employee/practice") for p in pages]
            with concurrent.futures.ThreadPoolExecutor(2) as pool:
                list(pool.map(lambda x: post(*x), zip(people,seeds)))
            assert query("SELECT count(*) FROM employee_store_orders") == [(3,)], "Concurrent seed duplicated"
            allocations = query("SELECT id,stock FROM products ORDER BY id")
            oid = query("SELECT MIN(order_id) FROM employee_store_orders")[0][0]
            claimpath = f"/handheld/employee/{oid}/claim"
            claims = [form(get(c),claimpath) for c in people]
            with concurrent.futures.ThreadPoolExecutor(2) as pool:
                results = list(pool.map(lambda x: post(*x),zip(people,claims)))
            winners = [i for i,r in enumerate(results) if r[0] == "/handheld/"]
            assert len(winners) == 1, "Atomic claim failed"
            winner = people[winners[0]]
            loser = people[1-winners[0]]
            assert query("SELECT count(*) FROM shopper_assignments WHERE order_id=? AND state='active'",(oid,)) == [(1,)]
            lid,sku = query("SELECT id,sku FROM working_order_items WHERE order_id=?",(oid,))[0]
            _,review = post(winner,form(get(winner,f"/handheld/?line={lid}"),"/handheld/scan"),{"code":sku})
            pick = form(review,"/handheld/pick")
            post(winner,pick,{"picked":"1"})
            post(winner,pick,{"picked":"1"})
            assert query("SELECT picked_quantity FROM working_order_items WHERE id=?",(lid,)) == [(1,)]
            # Correct the saved absolute total back to zero after reviewing afresh.
            _,review = post(winner,form(get(winner,f"/handheld/?line={lid}"),"/handheld/scan"),{"code":sku})
            post(winner,form(review,"/handheld/pick"),{"picked":"0"})
            assert query("SELECT picked_quantity FROM working_order_items WHERE id=?",(lid,)) == [(0,)]
            release = form(get(winner),f"/handheld/employee/{oid}/release")
            post(winner,release)
            post(loser,form(get(loser),claimpath))
            assert query("SELECT picked_quantity FROM working_order_items WHERE id=?",(lid,)) == [(0,)]
            assert query("SELECT id,stock FROM products ORDER BY id") == allocations, "Picking/release duplicated stock"
            _,review = post(loser,form(get(loser,f"/handheld/?line={lid}"),"/handheld/scan"),{"code":sku})
            post(loser,form(review,"/handheld/pick"),{"picked":"1"})
            ready = form(get(loser),f"/handheld/employee/{oid}/ready")
            post(loser,ready)
            post(loser,ready)
            assert query("SELECT status FROM orders WHERE id=?",(oid,)) == [("Ready",)]
            process.terminate(); process.wait(timeout=5); process=None
            process=start()
            post(people[0],seeds[0])
            assert query("SELECT count(*) FROM orders") == [(3,)], "Restart/retry regenerated orders"
            assert query("SELECT id,stock FROM products ORDER BY id") == allocations
            assert len(query("SELECT id FROM shopper_assignments WHERE order_id=? AND state='active'",(oid,)))==0
            print("Employee process smoke passed: separate identities; shared queue; simultaneous one-time seed and atomic claims; native same-origin forms; exact retry; pick/unpick; release/reclaim; ready/retry; restart retention; unchanged allocations.")
        finally:
            if process is not None:
                process.terminate(); process.wait(timeout=5)
            log.close()

if __name__ == "__main__":
    main()
