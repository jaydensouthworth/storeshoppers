#!/usr/bin/env python3
"""Rendered-form HTTP/restart smoke on disposable fake data only.

With --previous-binary, keep an actual schema10 process alive while the current schema
migrates its database, then prove its old Ready form cannot bypass a new hold.
No browser/visual or operational deployment claim is made.
"""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.parse
from weighted_smoke import Client, Page, PASSWORD, ROOT


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--previous-binary', type=Path)
    parser.add_argument('--previous-source', default='caller-supplied schema10')
    args = parser.parse_args()
    binary = ROOT / 'bin/shop'
    assert binary.is_file()
    evidence = Path(tempfile.mkdtemp(prefix='shopper-attention-smoke-'))
    path = evidence / 'fake.db'
    processes = []
    checks = []

    def query(sql, values=()):
        with sqlite3.connect(path.as_uri() + '?mode=ro', uri=True) as db:
            db.execute('PRAGMA query_only=ON')
            return db.execute(sql, values).fetchall()

    def fingerprint():
        records = query('SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name')
        for (table,) in query("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name"):
            escaped = '"' + table.replace('"', '""') + '"'
            records += [(table, sorted(map(repr, query('SELECT * FROM ' + escaped))))]
        return hashlib.sha256(repr(records).encode()).hexdigest()

    def start(executable, label):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        env = dict(os.environ, APP_ADDR=f'127.0.0.1:{port}', APP_ORIGIN=origin,
                   DATABASE_PATH=str(path), MANAGER_PASSWORD=PASSWORD,
                   DEMO_MODE='true', GOMAXPROCS='2')
        with (evidence / (label + '.log')).open('wb') as log:
            process = subprocess.Popen([str(executable.resolve())], env=env, stdout=log, stderr=log)
        processes.append(process)
        client = Client(origin)
        for _ in range(100):
            try:
                if client.request('/healthz')[0] == 200:
                    return process, client
            except OSError:
                pass
            assert process.poll() is None, (evidence / (label + '.log')).read_text()
            time.sleep(.05)
        raise AssertionError('startup timeout')

    def create_order(client, picked=False):
        _, home = client.get('/')
        _, cart = client.submit(home, 'add-1', {'quantity': '2', 'return': 'cart'})
        receipt, _ = client.submit(cart, 'place-order')
        oid = int(receipt.rsplit('/', 1)[1])
        _, ticket = client.get('/manager' + receipt)
        if picked:
            _, ticket = client.submit(ticket, 'advance-order')
            _, ticket = client.submit(ticket, 'save-picked-1', {'picked': '2'})
        return oid, ticket

    try:
        old_client = old_ticket = None
        if args.previous_binary:
            old_process, old_client = start(args.previous_binary, 'previous10')
            old_client.login()
            oid, old_ticket = create_order(old_client, True)
            assert query('SELECT MAX(version) FROM schema_version') == [(10,)]
            old_snapshot = {}
            for (table,) in query("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name<>'schema_version'"):
                escaped = '"' + table.replace('"', '""') + '"'
                cols = [row[1] for row in query('PRAGMA table_info(' + escaped + ')')]
                sql = 'SELECT ' + ','.join('"' + col + '"' for col in cols) + ' FROM ' + escaped
                old_snapshot[sql] = sorted(map(repr, query(sql)))
            process, owner = start(binary, 'current')
            owner.opener = old_client.opener  # Same authorized session cookie on loopback.
            for sql, rows in old_snapshot.items():
                assert sorted(map(repr, query(sql))) == rows, ('upgrade changed prior content', sql)
            checks.append('real populated10 additive upgrade preserves all original fields')
        else:
            process, owner = start(binary, 'current')
            owner.login()
            oid, _ = create_order(owner, True)
        assert query('SELECT MAX(version) FROM schema_version') == [(15,)]
        assert query('PRAGMA foreign_key_check') == []
        context = {'q': 'Apples & <review>', 'state': 'Picking', 'held': 'held',
                   'assigned': 'unassigned', 'sort': 'oldest', 'page': '2'}
        ticket_path = f'/manager/orders/{oid}'
        context_path = ticket_path + '?' + urllib.parse.urlencode(context)
        _, page = owner.get(context_path)
        original = query('SELECT * FROM order_items WHERE order_id=?', (oid,))
        stock = query('SELECT id,stock,version FROM products ORDER BY id')
        hold = copy.deepcopy(page.forms['place-order-hold'])
        reason = 'Private <receiving> & manager reason'
        result_url, page = owner.post(hold, {'reason': reason})
        assert urllib.parse.parse_qs(urllib.parse.urlparse(result_url).query) == {k: [v] for k, v in context.items()}
        page.contains('NEEDS ATTENTION', reason, 'Release hold', 'Edit picked count', 'Cancel the entire order')
        assert query('SELECT status,attention_reason FROM orders WHERE id=?', (oid,)) == [('Picking', reason)]
        assert query('SELECT id,stock,version FROM products ORDER BY id') == stock
        before = fingerprint()
        owner.post(hold, {'reason': reason}, hx=True)
        assert fingerprint() == before
        owner.post(hold, {'reason': 'Changed same key'}, hx=True, problem=True)
        assert fingerprint() == before
        # Disabled HTML is only guidance: server rejects a forged/saved Ready form.
        owner.submit(page, 'advance-order', hx=True, problem=True)
        assert fingerprint() == before
        _, page = owner.submit(page, 'add-order-note', {'reason': 'Private <note> about packaging'}, hx=True)
        page.contains('Private manager note saved.')
        _, tracker = owner.get(f'/orders/{oid}/status', hx=True)
        tracker.contains('Under manager review')
        assert 'receiving' not in tracker.html and 'packaging' not in tracker.html
        assert 'Manager only' not in tracker.html
        _, customer_list = owner.get('/orders')
        assert 'receiving' not in customer_list.html
        other = Client(owner.origin)
        other.login()
        for route in [ticket_path, ticket_path + '/products?context=add', f'/orders/{oid}/status']:
            assert other.request(route)[0] == 404
        _, other_queue = other.get('/manager/orders?held=held&page=999')
        other_queue.contains('Needs attention · 0')
        assert reason not in other_queue.html
        checks.append('HTML/HTMX hold replay, changed key, forged Ready, private notes and foreign scope')
        if old_client:
            before = fingerprint()
            old_form = old_ticket.forms['advance-order']
            assert old_client.request(old_form['action'], old_form['fields'])[0] == 503
            assert old_client.request('/healthz')[0] == 503
            assert fingerprint() == before
            assert old_process.poll() is None
            checks.append('still-running actual schema10 Ready and readiness return503 with exact preservation')
        # Interrupted stale release recovers entered text, never automatically repeats.
        _, page = owner.get(context_path)
        release = copy.deepcopy(page.forms['release-order-hold'])
        assert page.controls['release-hold-reason'].get('required') is None
        owner.post(release)
        assert query('SELECT attention_reason,attention_since FROM orders WHERE id=?', (oid,)) == [('', 0)]
        stale = copy.deepcopy(release)
        stale['fields']['command_key'] += '-stale'
        _, recovered = owner.post(stale, {'reason': 'Unsaved <release> note'}, hx=True, problem=True)
        recovered.contains('Your review change has not been saved.', 'Unsaved <release> note')
        _, page = owner.get(context_path)
        owner.submit(page, 'place-order-hold', {'reason': 'Second private hold'}, hx=True)
        process.terminate()
        process.wait(timeout=5)
        process, restarted = start(binary, 'restarted')
        restarted.opener = owner.opener
        owner = restarted
        _, page = owner.get(context_path)
        page.contains('Second private hold', 'Private <note> about packaging')
        assert query('SELECT * FROM order_items WHERE order_id=?', (oid,)) == original
        assert query('SELECT id,stock,version FROM products ORDER BY id') == stock
        owner.submit(page, 'release-order-hold')
        _, page = owner.get(context_path)
        _, page = owner.submit(page, 'advance-order', hx=True)
        page.contains('Ready')
        checks.append('optional release note, stale draft recovery, rehold, restart and Ready after release')
        # A held partial order can still be cancelled, with one stock return.
        second, page = create_order(owner)
        _, page = owner.submit(page, 'place-order-hold', {'reason': 'Cancel review test'}, hx=True)
        cancel = copy.deepcopy(page.forms['order-cancel'])
        cancel_stock = query('SELECT stock FROM products WHERE id=1')[0][0]
        owner.post(cancel, {'reason': 'Fake order no longer needed', 'disposition': 'restock'}, hx=True)
        once = fingerprint()
        owner.post(cancel, {'reason': 'Fake order no longer needed', 'disposition': 'restock'}, hx=True)
        assert fingerprint() == once
        assert query('SELECT stock FROM products WHERE id=1') == [(cancel_stock + 2,)]
        assert query('SELECT status,attention_reason,final_total FROM orders WHERE id=?', (second,)) == [('Completed', '', 0)]
        checks.append('held cancellation closes hold and restocks allocation exactly once')
        result = {'result': 'passed', 'current_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
                  'previous_source': args.previous_source if args.previous_binary else None,
                  'previous_sha256': hashlib.sha256(args.previous_binary.read_bytes()).hexdigest() if args.previous_binary else None,
                  'checks': checks, 'evidence': str(evidence)}
        (evidence / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result))
    finally:
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=5)


if __name__ == '__main__':
    main()
