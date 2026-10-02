#!/usr/bin/env python3
"""Isolated real-process structured replacement acceptance, using rendered forms.
Independent cookie jars. SQL reads only. No live data or camera claims.
"""
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
from handheld_smoke import QuietClient
from weighted_smoke import ROOT, PASSWORD, Page
from order_messages_smoke import raw_request


def main():
    with tempfile.TemporaryDirectory(prefix="shopper-substitution-process-") as directory:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0)); port=sock.getsockname()[1]
        origin=f"http://127.0.0.1:{port}"
        path=Path(directory)/"fake.db"
        env=dict(os.environ,APP_ADDR=f"127.0.0.1:{port}",APP_ORIGIN=origin,DATABASE_PATH=str(path),MANAGER_PASSWORD=PASSWORD,DEMO_MODE="true",GOMAXPROCS="2")
        customer,employee,other=(QuietClient(origin) for _ in range(3))
        process=None
        def query(sql,args=()):
            with sqlite3.connect(path.as_uri()+"?mode=ro",uri=True) as db:
                db.execute("PRAGMA query_only=ON")
                return db.execute(sql,args).fetchall()
        def start():
            with (Path(directory)/"server.log").open("ab") as log:
                p=subprocess.Popen([str(ROOT/"bin/shop")],env=env,stdout=log,stderr=log)
            for _ in range(100):
                try:
                    if customer.request("/healthz")[0]==200:return p
                except OSError:pass
                assert p.poll() is None,"Temporary substitution server exited"
                time.sleep(.05)
            raise AssertionError("Temporary server did not start")
        def form(page,route):
            forms=[f for f in page.all_forms if f["action"]==route]
            assert len(forms)==1,"Expected exactly one rendered action"
            return forms[0]
        def send(client,f,changes=None):
            values=dict(f["fields"],**(changes or {}))
            code,headers,body=raw_request(client,f["action"],values)
            assert code==303,"Expected persisted command and native redirect"
            return values
        try:
            process=start()
            _,home=customer.get("/")
            customer.submit(home,"add-1",{"quantity":"2","return":"cart"})
            _,home=customer.get("/")
            _,cart=customer.submit(home,"add-2",{"quantity":"1","return":"cart"})
            receipt_url,_=customer.submit(cart,"place-order",{"instructions":"Fake substitution test only"})
            oid=int(receipt_url.rsplit("/",1)[1]);customerpath=f"/orders/{oid}/substitutions"
            receipt=query("SELECT * FROM order_items WHERE order_id=? ORDER BY product_id",(oid,))
            total=query("SELECT total FROM orders WHERE id=?",(oid,))
            _,dash=employee.get("/handheld/employee")
            employee.post(form(dash,f"/handheld/employee/{oid}/claim"))
            line=query("SELECT id FROM working_order_items WHERE order_id=? AND product_id=1",(oid,))[0][0]
            stocks=query("SELECT id,stock FROM products WHERE id IN (1,3) ORDER BY id")
            _,page=employee.get(f"/handheld/substitutions?line={line}")
            _,preview=employee.post(form(page,"/handheld/substitutions/preview"),{"replacement_id":"3","quantity":"2","disposition":"restock","note":"Fake original returned and resalable"})
            assert query("SELECT COUNT(*) FROM substitution_proposals")==[(0,)],"Preview persisted"
            request=form(preview,"/handheld/substitutions/send")
            values=send(employee,request)
            assert raw_request(employee,request["action"],values)[0]==303,"Proposal retry failed"
            assert query("SELECT COUNT(*) FROM substitution_proposals")==[(1,)],"Duplicate request"
            assert query("SELECT id,stock FROM products WHERE id IN (1,3) ORDER BY id")==stocks,"Proposal reserved stock"
            assert raw_request(other,customerpath)[0]==403,"Stranger saw customer decision"
            # Continue the same task: rotating the device grant must retain approval.
            _,dash=employee.get("/handheld/employee")
            employee.post(form(dash,f"/handheld/employee/{oid}/claim"))
            # An unrelated pick may proceed while the customer decides.
            otherline,sku=query("SELECT id,sku FROM working_order_items WHERE order_id=? AND product_id=2",(oid,))[0]
            _,item=employee.get(f"/handheld/?line={otherline}")
            _,scan=employee.post(form(item,"/handheld/scan"),{"code":sku})
            employee.post(form(scan,"/handheld/pick"),{"picked":"1"})
            _,customerpage=customer.get(customerpath)
            assert "Approve this exact replacement" in customerpage.html,"Unrelated pick invalidated approval"
            decision=form(customerpage,customerpath)
            chosen=send(customer,decision,{"decision":"approve"})
            assert raw_request(customer,customerpath,chosen)[0]==303,"Decision replay failed"
            assert query("SELECT status FROM substitution_proposals")==[("approved",)]
            assert query("SELECT id,stock FROM products WHERE id IN (1,3) ORDER BY id")==[(1,stocks[0][1]+2),(3,stocks[1][1]-2)],"Stock not exchanged once"
            assert query("SELECT * FROM order_items WHERE order_id=? ORDER BY product_id",(oid,))==receipt
            assert query("SELECT total FROM orders WHERE id=?",(oid,))==total
            assert query("SELECT COUNT(*) FROM order_events WHERE order_id=? AND command_key LIKE 'customer-substitution:%'",(oid,))==[(1,)]
            lid,sku=query("SELECT id,sku FROM working_order_items WHERE order_id=? AND product_id=3",(oid,))[0]
            _,item=employee.get(f"/handheld/?line={lid}")
            _,scan=employee.post(form(item,"/handheld/scan"),{"code":sku})
            employee.post(form(scan,"/handheld/pick"),{"picked":"2"})
            # A later request is explicitly rejected without stock movement.
            _,page=employee.get(f"/handheld/substitutions?line={lid}")
            _,preview=employee.post(form(page,"/handheld/substitutions/preview"),{"replacement_id":"4","quantity":"1","disposition":"restock","note":"Fake alternative for rejection test"})
            send(employee,form(preview,"/handheld/substitutions/send"))
            _,page=customer.get(customerpath)
            stock_before=query("SELECT id,stock FROM products ORDER BY id")
            send(customer,form(page,customerpath),{"decision":"reject"})
            assert query("SELECT id,stock FROM products ORDER BY id")==stock_before,"Rejection mutated stock"
            assert query("SELECT status FROM substitution_proposals ORDER BY id")==[("approved",),("rejected",)]
            process.terminate();process.wait(timeout=5);process=None;process=start()
            _,page=customer.get(customerpath)
            assert "Approved · replacement applied" in page.html and "Customer rejected" in page.html,"Restart lost outcomes"
            assert query("SELECT id,stock FROM products ORDER BY id")==stock_before
            print("Substitution process smoke passed: independent customers/employees; preview/send/retry; no premature allocation; owner-only decision; Continue rotation; unrelated picking; approve once; original receipt; normal replacement scan/pick; explicit reject; restart retention.")
        finally:
            if process is not None:process.terminate();process.wait(timeout=5)

if __name__=="__main__":main()
