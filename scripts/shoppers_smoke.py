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

            def request(path, fields=None, hx=False, problem=False, actor=None):
                headers = {'Origin': origin}
                if hx:
                    headers['HX-Request'] = 'true'
                data = None if fields is None else urllib.parse.urlencode(fields).encode()
                with (actor or client).open(urllib.request.Request(origin + path, data=data, headers=headers), timeout=5) as response:
                    page = Page(response.read().decode())
                    assert page.problem == problem, page.text
                    return response.geturl().replace(origin, ''), page, response.headers

            def submit(page, button, changes=None, hx=False, problem=False):
                form = page.forms[button]
                return request(form['action'], dict(form['fields'], **(changes or {})), hx, problem)

            def wait_ready():
                for _ in range(100):
                    try:
                        urllib.request.urlopen(origin + '/healthz', timeout=1).close()
                        return
                    except OSError:
                        if proc.poll() is not None:
                            raise AssertionError('demo server exited')
                        time.sleep(0.05)
                raise AssertionError('demo server did not become ready')

            wait_ready()
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
            # Manage a scoped identity using only rendered forms. A duplicate
            # successful POST reuses the identity even after subsequent edits.
            _, page, _ = request('/manager/shoppers?new=1')
            create_form = page.forms['save-shopper-profile']
            create_fields = dict(create_form['fields'], name='Morgan Demo', initials='',
                                 availability='available', capacity='1')
            _, page, headers = request(create_form['action'], create_fields, hx=True)
            person = int(urllib.parse.parse_qs(urllib.parse.urlparse(headers['HX-Push-Url']).query)['person'][0])
            _, page, _ = request(create_form['action'], create_fields, hx=True)
            assert page.forms['save-shopper-profile']['fields']['initials'] == 'MD'
            _, page, _ = request(f'/manager/shoppers?order={order_id}')
            _, page, _ = submit(page, 'save-shopper-task', {'action': 'reassign', 'shopper_id': str(person), 'reason': 'Assign the custom demo identity'})
            _, page, _ = request('/')
            _, page, _ = submit(page, 'add-1', {'quantity': '1', 'return': 'cart'})
            next_receipt, page, _ = submit(page, 'place-order')
            next_order = int(next_receipt.rsplit('/', 1)[1])
            with sqlite3.connect(db_path) as db:
                roster_stock = db.execute('SELECT id,stock,version FROM products ORDER BY id').fetchall()
                roster_receipts = db.execute('SELECT * FROM order_items ORDER BY order_id,product_id').fetchall()
            _, page, _ = request(f'/manager/shoppers?order={next_order}')
            _, page, _ = submit(page, 'save-shopper-task', {'shopper_id': str(person), 'reason': 'Attempt capacity-limited assignment'}, hx=True, problem=True)
            assert 'active-order capacity' in page.text and 'Unassigned.' in page.text
            _, page, _ = request(f'/manager/shoppers?person={person}')
            stale_profile = dict(page.forms['save-shopper-profile']['fields'])
            _, page, _ = submit(page, 'save-shopper-profile', {'name': 'Morgan Renamed', 'initials': 'MR', 'availability': 'break'}, hx=True)
            assert 'Existing assignments and recorded picking progress remain unchanged' in page.text
            _, page, _ = request(f'/manager/shoppers/roster/{person}', dict(stale_profile, name='Unsaved <draft>'), hx=True, problem=True)
            assert 'Unsaved &lt;draft&gt;' in page.html and page.forms['save-shopper-profile']['fields']['version'] == '2'
            _, page, _ = request('/manager/shoppers?status=active&q=Morgan+Renamed')
            assert '1 result' in page.text and 'Morgan Demo' in page.text, page.text
            _, page, _ = request(f'/manager/shoppers?order={next_order}')
            _, page, _ = submit(page, 'save-shopper-task', {'shopper_id': str(person), 'reason': 'Attempt assignment while on break'}, hx=True, problem=True)
            assert 'Return them to Available' in page.text
            _, page, _ = request(f'/manager/shoppers?person={person}')
            _, page, _ = submit(page, 'archive-shopper', hx=True, problem=True)
            assert 'still has active work' in page.text
            _, page, _ = request(f'/manager/shoppers?order={order_id}')
            _, page, _ = submit(page, 'save-shopper-task', {'action': 'reassign', 'shopper_id': '2', 'reason': 'Hand over existing work while unavailable'}, hx=True)
            _, page, _ = request(f'/manager/shoppers?person={person}')
            _, page, headers = submit(page, 'archive-shopper', hx=True)
            assert 'person=' not in headers['HX-Push-Url'] and f'shopper-card-{person}' not in page.ids
            _, page, _ = request(f'/manager/shoppers?roster_status=archived&person={person}')
            assert f'shopper-card-{person}' in page.ids
            _, page, _ = submit(page, 'restore-shopper', hx=True)
            assert page.forms['save-shopper-profile']['fields']['availability'] == 'break'
            _, page, _ = submit(page, 'save-shopper-profile', {'availability': 'available', 'capacity': '1'}, hx=True)
            _, page, _ = request(f'/manager/shoppers?order={next_order}')
            _, page, _ = submit(page, 'save-shopper-task', {'shopper_id': str(person), 'reason': 'Resume available custom shopper'}, hx=True)
            assert 'Morgan Renamed' in page.text

            # Another visitor cannot see identity edits, workload or profile
            # audit, even with the same intentionally shared demo password.
            other = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))
            _, other_home, _ = request('/', actor=other)
            other_csrf = other_home.forms['add-1']['fields']['csrf']
            request('/manager/login', {'csrf': other_csrf, 'password': 'password'}, actor=other)
            _, other_page, _ = request('/manager/shoppers?roster_status=all', actor=other)
            assert 'Morgan Demo' not in other_page.text and 'Morgan Renamed' not in other_page.text
            assert f'shopper-card-{person}' not in other_page.ids
            with sqlite3.connect(db_path) as db:
                assert db.execute('SELECT id,stock,version FROM products ORDER BY id').fetchall() == roster_stock
                assert db.execute('SELECT * FROM order_items ORDER BY order_id,product_id').fetchall() == roster_receipts
                assert db.execute('SELECT count(*) FROM shoppers').fetchone()[0] == 4
                snapshot = {table: db.execute(f'SELECT * FROM {table} ORDER BY rowid').fetchall()
                            for table in ('shoppers', 'shopper_roster_profiles', 'shopper_roster_events',
                                          'shopper_assignments', 'shopper_event_links')}
                assert db.execute('SELECT to_shopper_name FROM shopper_event_links WHERE to_shopper_id=? ORDER BY event_id', (person,)).fetchall() == [('Morgan Demo',), ('Morgan Renamed',)]
            proc.terminate()
            proc.wait(timeout=10)
            proc = subprocess.Popen([str(ROOT / 'bin/shop')], env=env,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            wait_ready()
            _, page, _ = request(f'/manager/shoppers?person={person}&order={next_order}')
            assert 'Morgan Renamed' in page.text and 'Currently assigned to' in page.text
            with sqlite3.connect(db_path) as db:
                for table, saved in snapshot.items():
                    assert db.execute(f'SELECT * FROM {table} ORDER BY rowid').fetchall() == saved, table
            print('Shopper form smoke passed: assignment lifecycle, scoped roster create/edit/replay, capacity and unavailable guards, stale draft recovery, archive/restore, immutable history, cross-visitor privacy and restart persistence')
        finally:
            proc.terminate()
            proc.wait(timeout=10)


if __name__ == '__main__':
    main()
