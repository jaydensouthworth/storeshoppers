#!/usr/bin/env python3
"""Prove real old/new application overlap against fresh, fake SQLite databases.

No build, git history, remote access or live database is used. Supply already
built old and current executables. Evidence is retained in a unique temporary
folder (or a unique child of --evidence-directory).
"""
import argparse
import hashlib
import json
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
from http.cookiejar import CookieJar

from order_overrides_smoke import Page


def quote_identifier(value):
    return '"' + value.replace('"', '""') + '"'


def available_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def logical_fingerprint(path):
    with sqlite3.connect(path) as db:
        records = [('schema', db.execute(
            'SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name').fetchall())]
        for (table,) in db.execute("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name"):
            rows = db.execute('SELECT * FROM ' + quote_identifier(table)).fetchall()
            records.append((table, sorted(map(repr, rows))))
    return hashlib.sha256(repr(records).encode()).hexdigest()


def original_snapshots(path):
    with sqlite3.connect(path) as db:
        snapshots = {}
        for (table,) in db.execute("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name<>'schema_version'"):
            columns = [row[1] for row in db.execute('PRAGMA table_info(' + quote_identifier(table) + ')')]
            query = 'SELECT ' + ','.join(map(quote_identifier, columns)) + ' FROM ' + quote_identifier(table)
            snapshots[table] = (query, sorted(map(repr, db.execute(query).fetchall())))
        sequences = dict(db.execute('SELECT name,seq FROM sqlite_sequence').fetchall())
    return snapshots, sequences


def start_server(binary, path, log_path):
    port = available_port()
    origin = f'http://127.0.0.1:{port}'
    env = dict(os.environ, APP_ADDR=f'127.0.0.1:{port}', APP_ORIGIN=origin,
               DATABASE_PATH=str(path), MANAGER_PASSWORD='local-demo-only', DEMO_MODE='true')
    log = log_path.open('w')
    process = subprocess.Popen([str(binary)], env=env, stdout=log, stderr=log)
    try:
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise AssertionError(f'{binary} failed: {log_path.read_text()}')
            try:
                with urllib.request.urlopen(origin + '/healthz', timeout=.5) as response:
                    if response.status == 200:
                        return process, origin, log
            except OSError:
                time.sleep(.03)
        raise AssertionError(f'{binary} startup timed out: {log_path}')
    except BaseException:
        process.terminate()
        process.wait(timeout=5)
        log.close()
        raise


def probe(label, old_binary, guarded, args, evidence, record):
    path = evidence / (label + '.db')
    assert not path.exists(), 'Never overwrite an existing proof database'
    old, origin, old_log = start_server(old_binary, path, evidence / (label + '-old.log'))
    current, current_log = None, None
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

    def request(route, fields=None):
        body = None if fields is None else urllib.parse.urlencode(fields).encode()
        req = urllib.request.Request(origin + route, data=body, headers={'Origin': origin})
        try:
            with client.open(req, timeout=8) as response:
                return response.status, response.geturl().replace(origin, ''), response.read().decode()
        except urllib.error.HTTPError as error:
            return error.code, route, error.read().decode()

    def get(route):
        status, url, markup = request(route)
        assert status == 200, (status, markup[:400])
        return url, Page(markup)

    def submit(page, button, changes=None):
        form = page.forms[button]
        status, url, markup = request(form['action'], dict(form['fields'], **(changes or {})))
        assert status == 200, (status, markup[:400])
        return url, Page(markup)

    try:
        _, home = get('/')
        csrf = home.forms['add-1']['fields']['csrf']
        assert request('/manager/login', {'csrf': csrf, 'password': 'local-demo-only'})[0] == 200

        def create_order(picked):
            _, page = get('/')
            _, page = submit(page, 'add-1', {'quantity': '2', 'return': 'cart'})
            url, page = submit(page, 'place-order')
            order_id = int(url.split('/')[-1])
            _, page = get('/manager' + url)
            _, page = submit(page, 'advance-order')
            if picked:
                _, page = submit(page, 'save-picked-1', {'picked': '2'})
            return order_id, page

        _, ready = create_order(True)
        _, picking = create_order(False)
        mixed_id, mixed = create_order(True)
        _, home = get('/')
        _, basket = submit(home, 'add-1', {'quantity': '1', 'return': 'cart'})
        checkout = basket.forms['place-order']
        csrf = home.forms['add-1']['fields']['csrf']
        snapshots, sequences = original_snapshots(path)
        with sqlite3.connect(path) as db:
            predecessor_version = db.execute('SELECT MAX(version) FROM schema_version').fetchone()[0]

        # The actual new application performs all real migrations while the old
        # HTTP process and its established SQLite connection remain running.
        current, current_origin, current_log = start_server(
            args.current_binary, path, evidence / (label + '-current.log'))
        with sqlite3.connect(path) as db:
            version = db.execute('SELECT MAX(version) FROM schema_version').fetchone()[0]
            assert version == args.schema_version, version
            for table, (query, expected) in snapshots.items():
                assert sorted(map(repr, db.execute(query).fetchall())) == expected, ('changed prior rows', table)
            current_sequences = dict(db.execute('SELECT name,seq FROM sqlite_sequence').fetchall())
            assert all(current_sequences.get(name) == seq for name, seq in sequences.items()), 'sequence high-water changed'
            assert db.execute('PRAGMA foreign_key_check').fetchall() == []
            tables = db.execute("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'").fetchall()
            fences = db.execute("SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND name LIKE ?",
                                (f'app_writer_v{version}_%',)).fetchone()[0]
            assert fences == len(tables) * 3, (fences, tables)
            # Test-only trusted fixture writer. The callback is the explicitly
            # requested compiled schema version, never read from the database.
            db.create_function('app_schema_version', 0, lambda: args.schema_version, deterministic=True)
            db.execute("INSERT INTO products(id,name,description,category,barcode,icon,price,stock,category_id,sku,sale_unit,price_basis,quantity_step) VALUES(1000,'Gram fixture','Fake weighted fixture','Produce','FENCE-1000','apple',349,900,1,'FENCE-1000','g',1000,1)")
            db.execute("INSERT INTO working_order_items(order_id,product_id,name,price,quantity,picked_quantity,sku,sale_unit,price_basis,quantity_step,allocated_quantity,measurement_confirmed) VALUES(?,1000,'Gram fixture',349,50,50,'FENCE-1000','g',1000,1,50,1)", (mixed_id,))
            db.execute("INSERT INTO order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,subtotal) VALUES(?,1000,'Gram fixture',349,50,'FENCE-1000','g',1000,1,17)", (mixed_id,))
            db.execute('UPDATE orders SET total=total+17 WHERE id=?', (mixed_id,))
        record(label + ' real migration', dict(predecessor_schema=predecessor_version,
               schema=version, fences=fences, old_process_running=old.poll() is None,
               original_rows_and_sequences_preserved=True))

        cases = [
            ('counted Ready', ready.forms['advance-order']['action'], ready.forms['advance-order']['fields']),
            ('mixed Ready', mixed.forms['advance-order']['action'], mixed.forms['advance-order']['fields']),
            ('picking', picking.forms['save-picked-1']['action'], dict(picking.forms['save-picked-1']['fields'], picked='1')),
            ('checkout', checkout['action'], checkout['fields']),
            ('logout', '/manager/logout', {'csrf': csrf}),
            ('reset', '/manager/demo/reset', {'csrf': csrf, 'confirm': 'reset-shared-demo'}),
        ]
        for name, route, fields in cases:
            before = logical_fingerprint(path)
            status, _, markup = request(route, fields)
            expected = 503 if guarded or name == 'reset' else 500
            assert status == expected, (label, name, status, markup[:400])
            assert logical_fingerprint(path) == before, (label, name, 'changed database')
            record(label + ' ' + name, dict(status=status, full_fingerprint_unchanged=True))
        assert not list(evidence.glob(path.name + '.demo-backups/*')), 'old reset created an archive'

        for route in ['/', '/manager/orders/' + str(mixed_id), '/healthz']:
            before = logical_fingerprint(path)
            status, _, markup = request(route)
            assert status in ([503] if guarded else [200, 500]), (label, route, status, markup[:400])
            assert logical_fingerprint(path) == before
            record(label + ' GET ' + route, dict(status=status, unchanged=True,
                   residual_old_read_or_readiness_risk=not guarded and status == 200))
        with sqlite3.connect(path) as db:
            row = db.execute('SELECT status,final_total,total FROM orders WHERE id=?', (mixed_id,)).fetchone()
            assert row == ('Picking', None, 715), row
        record(label + ' incorrect final prevented', dict(preserved_state=row,
               unsafe_old_final_cents=18148, correct_measured_final_cents=715))
        with urllib.request.urlopen(current_origin + '/healthz', timeout=3) as response:
            assert response.status == 200
    finally:
        for process in (current, old):
            if process is not None:
                process.terminate()
                process.wait(timeout=5)
        old_log.close()
        if current_log is not None:
            current_log.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for role in ('unguarded', 'guarded', 'current'):
        parser.add_argument('--' + role + '-binary', required=True, type=Path)
    parser.add_argument('--schema-version', required=True, type=int)
    parser.add_argument('--additional-guarded-binary', type=Path, help='An additional predecessor such as the image-only schema-9 application')
    parser.add_argument('--additional-guarded-source', help='Source commit for --additional-guarded-binary')
    parser.add_argument('--unguarded-source', default='b7c083a9a142ebc8bd1e6d61b1865002e937e133')
    parser.add_argument('--guarded-source', default='0c4719e')
    parser.add_argument('--current-source', default='caller-supplied working tree')
    parser.add_argument('--evidence-directory', type=Path, help='Existing parent folder for a unique evidence subdirectory')
    args = parser.parse_args()
    if args.schema_version < 10:
        parser.error('integrated weighted schema version must be at least 10')
    if bool(args.additional_guarded_binary) != bool(args.additional_guarded_source):
        parser.error('additional guarded binary and source must be supplied together')
    for role in ('unguarded', 'guarded', 'current'):
        path = getattr(args, role + '_binary').resolve(strict=True)
        if not path.is_file() or not os.access(path, os.X_OK):
            parser.error(str(path) + ' must be an executable file')
        setattr(args, role + '_binary', path)
    if args.additional_guarded_binary is not None:
        args.additional_guarded_binary = args.additional_guarded_binary.resolve(strict=True)
        if not args.additional_guarded_binary.is_file() or not os.access(args.additional_guarded_binary, os.X_OK):
            parser.error('additional guarded binary must be an executable file')
    evidence = Path(tempfile.mkdtemp(prefix='shopper-weighted-overlap-', dir=args.evidence_directory))
    results = []

    def record(name, detail):
        result = {'test': name, 'detail': detail}
        results.append(result)
        (evidence / 'results.json').write_text(json.dumps(results, indent=2) + '\n')
        print(json.dumps(result), flush=True)

    executables = {role: dict(path=str(getattr(args, role + '_binary')),
           source=getattr(args, role + '_source'),
           sha256=hashlib.sha256(getattr(args, role + '_binary').read_bytes()).hexdigest())
           for role in ('unguarded', 'guarded', 'current')}
    if args.additional_guarded_binary is not None:
        executables['additional-guarded'] = dict(path=str(args.additional_guarded_binary),
            source=args.additional_guarded_source,
            sha256=hashlib.sha256(args.additional_guarded_binary.read_bytes()).hexdigest())
    record('executables', executables)
    print('Evidence: ' + str(evidence), flush=True)
    predecessors = [('unguarded', args.unguarded_binary, False), ('guarded', args.guarded_binary, True)]
    if args.additional_guarded_binary is not None:
        predecessors.append(('additional-guarded', args.additional_guarded_binary, True))
    for label, binary, guarded in predecessors:
        probe(label, binary, guarded, args, evidence, record)
    record('compatibility limitation', 'Legacy writes are fenced; guarded predecessors refuse incompatible reads and health. An ancient unguarded process can still serve errors or stale reads, so live availability is verified separately.')


if __name__ == '__main__':
    main()
