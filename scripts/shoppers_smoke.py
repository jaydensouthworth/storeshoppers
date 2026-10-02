#!/usr/bin/env python3
"""Exercise rendered shopper forms against a real disposable demo server."""
import os
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request
from http.cookiejar import CookieJar
from order_overrides_smoke import Page, ROOT


def main():
    with tempfile.TemporaryDirectory(prefix='shop-shoppers-html-') as directory:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        db_path = directory + '/shop.db'
        env = dict(os.environ, APP_ADDR=f'127.0.0.1:{port}', APP_ORIGIN=origin,
                   DATABASE_PATH=db_path, MANAGER_PASSWORD='password', DEMO_MODE='true')
        proc = subprocess.Popen([str(ROOT / 'bin/shop')], env=env,
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

            def request(path, fields=None, hx=False, problem=False):
                headers = {'Origin': origin}
                if hx:
                    headers['HX-Request'] = 'true'
                data = None if fields is None else urllib.parse.urlencode(fields).encode()
                with client.open(urllib.request.Request(origin + path, data=data, headers=headers)) as response:
                    page = Page(response.read().decode())
                    assert page.problem == problem, page.text
                    return response.geturl().replace(origin, ''), page, response.headers

            def submit(page, button, changes=None, hx=False, problem=False):
                form = page.forms[button]
                return request(form['action'], dict(form['fields'], **(changes or {})), hx, problem)

            for _ in range(100):
                try:
                    urllib.request.urlopen(origin + '/healthz').close()
                    break
                except OSError:
                    if proc.poll() is not None:
                        raise AssertionError('demo server exited')
                    time.sleep(0.05)
            _, page, _ = request('/')
            _, page, _ = submit(page, 'add-1', {'quantity': '2', 'return': 'cart'})
            receipt, page, _ = submit(page, 'place-order')
            order_id = int(receipt.rsplit('/', 1)[1])
            _, home, _ = request('/')
            csrf = home.forms['add-1']['fields']['csrf']
            request('/manager/login', {'csrf': csrf, 'password': 'password'})
            with sqlite3.connect(db_path) as db:
                stock = db.execute('SELECT id,stock,version FROM products ORDER BY id').fetchall()
                receipt_snapshot = db.execute('SELECT * FROM order_items WHERE order_id=?', (order_id,)).fetchall()
            selected = f'/manager/shoppers?status=unassigned&q=not-visible&order={order_id}'
            _, page, _ = request(selected)
            assert '0 results' in page.text and 'save-shopper-task' in page.forms
            assert 'Scans per minute' in page.text and 'Unrecorded' in page.text
            original_form = dict(page.forms['save-shopper-task']['fields'])
            _, page, _ = submit(page, 'save-shopper-task', {'shopper_id': '1', 'reason': 'Assign demo shopping task'})
            assert 'Currently assigned to' in page.text and 'Avery Morgan' in page.text
            path = f'/manager/shoppers/orders/{order_id}'
            _, replay, _ = request(path, dict(original_form, shopper_id='1', reason='Assign demo shopping task'), hx=True)
            assert 'Shopper assignment saved' in replay.text
            _, page, headers = submit(page, 'save-shopper-task', {'action': 'reassign', 'shopper_id': '2', 'reason': 'Balance demo task workload'}, hx=True)
            assert headers.get('HX-Push-Url') and 'Jordan Lee' in page.text
            stale_form = dict(page.forms['save-shopper-task']['fields'])
            _, page, _ = submit(page, 'save-shopper-task', {'action': 'cancel', 'reason': 'Pause this task only'}, hx=True)
            assert 'Unassigned.' in page.text and 'Cancelled order' not in page.text
            _, draft, _ = request(path, dict(stale_form, action='reassign', shopper_id='3', reason='Keep & review <draft>'), hx=True, problem=True)
            assert 'Your change was not saved' in draft.text and 'Keep &amp; review &lt;draft&gt;' in draft.html
            _, closed, _ = request('/manager/shoppers?status=unassigned')
            assert 'assignment-editor' not in closed.ids
            _, page, _ = request(f'/manager/shoppers?order={order_id}')
            _, page, _ = submit(page, 'save-shopper-task', {'shopper_id': '3', 'reason': 'Resume demo shopping task'})
            _, ticket, _ = request('/manager' + receipt)
            assert 'SHOPPER ASSIGNMENT' in ticket.text and 'Casey Rivera' in ticket.text
            with sqlite3.connect(db_path) as db:
                assert db.execute('SELECT id,stock,version FROM products ORDER BY id').fetchall() == stock
                assert db.execute('SELECT * FROM order_items WHERE order_id=?', (order_id,)).fetchall() == receipt_snapshot
                assert db.execute("SELECT count(*) FROM shopper_assignments WHERE order_id=? AND state='active'", (order_id,)).fetchone()[0] == 1
                assert db.execute('SELECT status,completion_kind FROM orders WHERE id=?', (order_id,)).fetchone() == ('Placed', '')
            print('Shopper form smoke passed: scoped selection, real assignments, replay, reassign, task cancellation, draft recovery, close, ticket links, valid labels and stock/receipt preservation')
        finally:
            proc.terminate()
            proc.wait(timeout=10)


if __name__ == '__main__':
    main()
