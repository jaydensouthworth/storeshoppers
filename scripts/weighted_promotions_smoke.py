#!/usr/bin/env python3
"""Actual-process per-kilogram promotions using rendered forms and read-only DB assertions."""
from datetime import datetime, timedelta, timezone
from pathlib import Path
import copy
import os
import socket
import sqlite3
import subprocess
import tempfile
import time
from weighted_smoke import Client, PASSWORD

ROOT = Path(__file__).resolve().parent.parent


def main():
    with tempfile.TemporaryDirectory(prefix='shopper-weighted-promotions-') as directory:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        db_path = Path(directory) / 'shop.db'
        env = dict(os.environ, APP_ADDR=f'127.0.0.1:{port}', APP_ORIGIN=origin,
                   DATABASE_PATH=str(db_path), MANAGER_PASSWORD=PASSWORD, DEMO_MODE='true', GOMAXPROCS='2')
        owner, process = Client(origin), None

        def query(sql, args=()):
            with sqlite3.connect(db_path.as_uri() + '?mode=ro', uri=True) as db:
                db.execute('PRAGMA query_only=ON')
                return db.execute(sql, args).fetchall()

        def start():
            proc = subprocess.Popen([str(ROOT / 'bin/shop')], env=env,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                for _ in range(100):
                    try:
                        if owner.request('/healthz')[0] == 200:
                            return proc
                    except OSError:
                        pass
                    assert proc.poll() is None, 'server exited'
                    time.sleep(.05)
                raise AssertionError('server did not start')
            except BaseException:
                proc.terminate(); proc.wait(timeout=5)
                raise

        try:
            process = start()
            owner.login()
            _, page = owner.get('/manager/catalog?tab=new')
            form = page.forms['create-product']
            category = next(v for v in form['choices']['category_id'] if v)
            owner.post(form, {'name': 'Loose demo pears', 'sku': 'PROMO-WEIGHT',
                             'description': 'Disposable fake weighted promotion product',
                             'category_id': category, 'type_id': '0', 'icon': 'apple',
                             'sale_unit': 'g', 'price': '499', 'quantity_step': '50'})
            pid = query("SELECT id FROM products WHERE sku='PROMO-WEIGHT'")[0][0]
            _, page = owner.get(f'/manager/stock?product={pid}')
            owner.submit(page, f'save-stock-{pid}', {'delta': '2000', 'reason': 'Fake promotion stock'})
            _, page = owner.get('/manager/promotions/new')
            page.contains('Loose demo pears · $4.99 per kg regular', 'per kilogram')
            form = page.forms['save-promotion']
            ref = next(v for v in form['choices']['product_ref'] if v.startswith(f'{pid}:'))
            now = datetime.now(timezone.utc).replace(second=0, microsecond=0)
            owner.post(form, {'product_ref': ref, 'sale_price_usd': '3.49',
                             'starts': (now-timedelta(minutes=1)).strftime('%Y-%m-%dT%H:%M'),
                             'ends': (now+timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M')})
            sale = query('SELECT id,sale_unit,price_basis,sale_price FROM promotions WHERE product_id=?', (pid,))[0]
            assert sale[1:] == ('g', 1000, 349)
            _, page = owner.get(f'/manager/featured?edit={pid}')
            owner.submit(page, 'save-featured', {'featured': '1'})
            _, home = owner.get('/')
            home.contains('$3.49', 'per kg', '2000 g available', 'Choose grams')
            assert f'weekly-form-{pid}' not in home.forms
            gram_link = home.controls[f'weekly-weight-{pid}']['href']
            _, detail = owner.get(gram_link)
            _, basket = owner.submit(detail, f'detail-add-{pid}', {'quantity': '500', 'return': 'cart'})
            basket.contains('500 g', '$1.75', 'ESTIMATE')
            order_url, _ = owner.submit(basket, 'place-order')
            oid = int(order_url.rsplit('/', 1)[1])
            original = query('SELECT quantity,price,price_basis,subtotal FROM order_items WHERE order_id=?', (oid,))
            assert original == [(500, 349, 1000, 175)]
            # Leave a separate held basket at the same sale quote, then cancel it.
            _, detail = owner.get(gram_link)
            _, basket = owner.submit(detail, f'detail-add-{pid}', {'quantity': '500', 'return': 'cart'})
            stale = copy.deepcopy(basket.forms['place-order'])
            _, page = owner.get(f'/manager/promotions/{sale[0]}')
            owner.post(page.action(f'/manager/promotions/{sale[0]}/cancel'))
            _, stale_page = owner.post(stale, hx=True, problem=True)
            stale_page.contains('Review the current basket', '$2.50')
            assert query('SELECT count(*) FROM orders') == [(1,)]
            assert query('SELECT stock FROM products WHERE id=?', (pid,)) == [(1000,)]
            _, home = owner.get('/')
            assert f'weekly-weight-{pid}' not in home.controls
            assert f'featured-weight-{pid}' in home.controls
            home.contains('$4.99', 'per kg', 'Choose grams')
            # Measured fulfillment keeps the confirmed sale rate after cancellation.
            ticket = '/manager' + order_url
            _, page = owner.get(ticket)
            lid = query('SELECT id FROM working_order_items WHERE order_id=?', (oid,))[0][0]
            _, preview = owner.submit(page, f'preview-actual-{lid}', {'actual': '527'}, hx=True)
            preview.contains('$3.49 per kg', '$1.84', 'Reserve 27 g more')
            confirm = copy.deepcopy(preview.forms[f'confirm-actual-{lid}'])
            owner.post(confirm, hx=True)
            before = query('SELECT stock FROM products WHERE id=?', (pid,))
            owner.post(confirm, hx=True)
            assert query('SELECT stock FROM products WHERE id=?', (pid,)) == before == [(973,)]
            assert query('SELECT quantity,allocated_quantity,picked_quantity,price,measurement_confirmed FROM working_order_items WHERE id=?', (lid,)) == [(500, 527, 527, 349, 1)]
            _, page = owner.get(ticket)
            owner.submit(page, 'advance-order')
            assert query('SELECT total,final_total,status FROM orders WHERE id=?', (oid,)) == [(175, 184, 'Ready')]
            process.terminate(); process.wait(timeout=5)
            process = start()
            _, receipt = owner.get(order_url)
            receipt.contains('$1.84', '$1.75', '527 g')
            assert query('SELECT quantity,price,price_basis,subtotal FROM order_items WHERE order_id=?', (oid,)) == original
            assert query('SELECT sale_unit,price_basis,cancelled FROM promotions WHERE id=?', (sale[0],)) == [('g', 1000, 1)]
            assert query('PRAGMA foreign_key_check') == []
            assert query('SELECT max(version) FROM schema_version') == [(13,)]
            print('Weighted promotions process smoke passed: manager gram catalog/sale/featured forms, compact Choose grams actions, held stale-quote recovery, immutable sale rate after cancellation, exact weight replay, final receipt and restart')
        finally:
            if process is not None and process.poll() is None:
                process.terminate(); process.wait(timeout=5)


if __name__ == '__main__':
    main()
