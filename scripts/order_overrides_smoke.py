#!/usr/bin/env python3
"""Actual-process manager workflows, bounded picker, retries and stock/receipt checks."""
import copy
import json
import os
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from http.cookiejar import CookieJar
from html.parser import HTMLParser

ROOT = Path(__file__).resolve().parent.parent

class Page(HTMLParser):
    def __init__(self, text):
        super().__init__()
        self.html, self.text = text, ''
        self.problem = False
        self.forms, self.hidden, self.loose, self.loose_choices = {}, {}, {}, {}
        self.current, self.select = None, None
        self.ids, self.refs, self.errors = set(), [], []
        self.feed(text)
        self.errors += ['missing referenced ID ' + ref for ref in self.refs if ref not in self.ids]
        assert not self.errors, self.errors
        assert 'ZgotmplZ' not in text

    def handle_starttag(self, tag, pairs):
        a = dict(pairs)
        if {'notice', 'error'}.issubset(a.get('class', '').split()) and 'hidden' not in a:
            self.problem = True
        if 'id' in a:
            if a['id'] in self.ids:
                self.errors.append('duplicate ID ' + a['id'])
            self.ids.add(a['id'])
        if tag == 'label' and 'for' in a:
            self.refs.append(a['for'])
        for ref in ('aria-labelledby', 'aria-describedby'):
            self.refs += a.get(ref, '').split()
        if tag == 'form':
            assert self.current is None, 'nested form'
            self.current = {'method': a.get('method', 'get'), 'action': a.get('action'), 'fields': {}, 'buttons': [], 'choices': {}}
        if tag in ('input', 'select', 'textarea') and a.get('name'):
            fields = self.current['fields'] if self.current is not None else self.loose
            choices = self.current['choices'] if self.current is not None else self.loose_choices
            if a.get('type') == 'radio':
                choices.setdefault(a['name'], []).append(a.get('value', ''))
                if 'checked' in a:
                    fields[a['name']] = a.get('value', '')
            else:
                fields[a['name']] = a.get('value', '')
            if a.get('type') == 'hidden':
                self.hidden.setdefault(a['name'], a.get('value', ''))
        if self.current is not None:
            if tag == 'select':
                self.select = a.get('name')
            if tag == 'option' and self.select and 'selected' in a:
                self.current['fields'][self.select] = a.get('value', '')
            if tag == 'button' and 'id' in a:
                self.current['buttons'].append(a)

    def handle_endtag(self, tag):
        if tag == 'select':
            self.select = None
        if tag == 'form' and self.current is not None:
            for button in self.current['buttons']:
                # Native submitters can override a shared form's destination
                # and contribute their own name/value (e.g. a basket Update).
                form = copy.deepcopy(self.current)
                form['action'] = button.get('formaction', form['action'])
                form['method'] = button.get('formmethod', form['method'])
                if button.get('name'):
                    form['fields'][button['name']] = button.get('value', '')
                self.forms[button['id']] = form
            self.current = None

    def handle_data(self, data):
        self.text += ' ' + data


def main():
    with tempfile.TemporaryDirectory(prefix='shop-manager-workflows-') as d:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        db_path = d + '/shop.db'
        env = dict(os.environ, APP_ADDR=f'127.0.0.1:{port}', APP_ORIGIN=origin,
                   DATABASE_PATH=db_path, MANAGER_PASSWORD='local-demo-only', DEMO_MODE='true')
        proc = subprocess.Popen([str(ROOT / 'bin/shop')], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        jar = CookieJar()
        client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
        def query(sql, args=()):
            with sqlite3.connect(db_path) as db:
                return db.execute(sql, args).fetchall()
        def stock(pid):
            return query('SELECT stock FROM products WHERE id=?', (pid,))[0][0]
        def get(path, hx=False):
            request = urllib.request.Request(origin + path, headers={'HX-Request':'true'} if hx else {})
            with client.open(request, timeout=5) as response:
                return response.geturl().removeprefix(origin), Page(response.read().decode())
        def raw_post(path, fields, hx=False):
            request = urllib.request.Request(origin + path, data=urllib.parse.urlencode(fields).encode(),
                headers={'Origin':origin, **({'HX-Request':'true'} if hx else {})})
            try:
                response = client.open(request, timeout=5)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                return response.status, response.headers, response.geturl().removeprefix(origin), response.read().decode()
        def post(path, fields, hx=False, problem=False):
            status, _, url, text = raw_post(path, fields, hx)
            assert status == 200, (status, text)
            page = Page(text)
            assert page.problem == problem, page.text
            return url, page
        def submit(page, button, changes=None, hx=False, problem=False):
            form = page.forms[button]
            fields = dict(form['fields'], **(changes or {}))
            for name, options in form['choices'].items():
                if name in fields:
                    assert str(fields[name]) in options, ('unrendered candidate', name, fields[name], options)
            if form['method'].lower() == 'get':
                return get(form['action'] + '?' + urllib.parse.urlencode(fields), hx)
            return post(form['action'], fields, hx, problem)
        def search(page, search_button, post_button, text, selected, hx=False):
            if not hx:
                _, result = submit(page, search_button, {'product_q':text})
                assert str(selected) in sum(result.forms[post_button]['choices'].values(), [])
                return result
            form = page.forms[search_button]
            _, fragment = get(form['action'] + '?' + urllib.parse.urlencode(dict(form['fields'], product_q=text)), True)
            assert not fragment.forms and 'order_version' not in fragment.loose and 'revision' not in fragment.loose
            assert 0 < sum(len(v) for v in fragment.loose_choices.values()) <= 6
            assert str(selected) in sum(fragment.loose_choices.values(), [])
            result = copy.deepcopy(page)
            result.forms[post_button]['fields'].update(fragment.loose)
            result.forms[post_button]['choices'] = fragment.loose_choices
            return result
        def placed_snapshot(oid):
            return query('SELECT product_id,name,price,quantity,subtotal FROM order_items WHERE order_id=? ORDER BY product_id',(oid,))
        def new_order():
            _, page = get('/')
            _, page = submit(page, 'add-1', {'quantity':'2', 'return':'cart'})
            return submit(page, 'place-order')
        try:
            for _ in range(100):
                try:
                    with urllib.request.urlopen(origin + '/healthz', timeout=1):
                        break
                except OSError:
                    assert proc.poll() is None, 'server exited'
                    time.sleep(.05)
            else:
                raise AssertionError('server not ready')
            initial = {pid:stock(pid) for pid in (1,2,3,4)}
            receipt, page = new_order()
            oid = int(receipt.rsplit('/',1)[-1]); original = placed_snapshot(oid)
            _, login = get('/manager/login')
            post('/manager/login', {'csrf':login.hidden['csrf'], 'password':'local-demo-only'})
            _, page = get('/manager' + receipt)
            line = next(key.removeprefix('order-set-') for key in page.forms if key.startswith('order-set-'))
            # Optional short note, unchanged receipt, and no disposition needed for an increase.
            _, page = submit(page, 'order-set-'+line, {'quantity':'3', 'reason':'ok', 'disposition':''}, True)
            assert stock(1) == initial[1]-3 and placed_snapshot(oid) == original
            assert query('SELECT reason FROM order_events WHERE order_id=? ORDER BY id DESC LIMIT 1',(oid,))[0][0] == 'Manager note: ok'
            page = search(page, 'order-add-search-button', 'order-add-item', 'sourdough', 3, True)
            _, page = submit(page, 'order-add-item', {'product_id':'3','quantity':'1','reason':''}, True)
            assert stock(3) == initial[3]-1
            # Real HTML fallback search, required original-stock outcome and selected radio.
            page = search(page, 'search-sub-button-'+line, 'order-substitute-'+line, 'spinach', 2)
            _, page = submit(page, 'order-substitute-'+line, {'replacement_id':'2','quantity':'2','quantity_2':'2','reason':'','disposition':'restock'})
            assert stock(1) == initial[1] and stock(2) == initial[2]-2 and placed_snapshot(oid) == original
            # Edit count can auto-start Placed, with an exact replay doing no extra work.
            form = copy.deepcopy(page.forms['save-picked-2'])
            fields = dict(form['fields'], picked='1')
            _, page = post(form['action'], fields, True)
            count = query('SELECT COUNT(*) FROM order_events WHERE order_id=?',(oid,))[0][0]
            post(form['action'], fields, True)
            assert query('SELECT status FROM orders WHERE id=?',(oid,))[0][0] == 'Picking'
            assert query('SELECT COUNT(*) FROM order_events WHERE order_id=?',(oid,))[0][0] == count
            assert stock(2) == initial[2]-2 and '33% shopped' in page.text
            _, page = submit(page, 'order-finish-partial', {'remainder':'unavailable','disposition':'writeoff','reason':'Unpicked demo items unavailable'})
            final = query('SELECT status,final_total,completion_kind FROM orders WHERE id=?',(oid,))[0]
            spinach_price = query('SELECT price FROM working_order_items WHERE order_id=? AND product_id=2',(oid,))[0][0]
            assert final == ('Completed',spinach_price,'partial') and placed_snapshot(oid) == original
            assert query('SELECT SUM(picked_quantity),SUM(quantity),SUM(unavailable_quantity) FROM working_order_items WHERE order_id=?',(oid,))[0] == (1,3,2)
            assert stock(2) == initial[2]-2 and stock(3) == initial[3]-1
            _, poll = get(receipt+'/status',True)
            assert '33% shopped' in poll.text and 'FINAL TOTAL' in poll.text and 'unavailable' in poll.text
            # One-tap Mark picked starts a second order; exact replay and cancel return once.
            receipt2,_ = new_order(); oid2=int(receipt2.rsplit('/',1)[-1]); _,page=get('/manager'+receipt2)
            mark = next(key for key in page.forms if key.startswith('mark-picked-'))
            form=copy.deepcopy(page.forms[mark]); _,page=post(form['action'],form['fields'],True)
            post(form['action'],form['fields'],True)
            assert query('SELECT status FROM orders WHERE id=?',(oid2,))[0][0]=='Picking'
            assert query('SELECT COUNT(*) FROM order_events WHERE order_id=?',(oid2,))[0][0]==1
            assert stock(1)==initial[1]-2
            cancel=copy.deepcopy(page.forms['order-cancel']); fields=dict(cancel['fields'],disposition='restock',reason='Cancel this demo order')
            _,page=post(cancel['action'],fields);post(cancel['action'],fields)
            assert stock(1)==initial[1] and query('SELECT final_total,completion_kind FROM orders WHERE id=?',(oid2,))[0]==(0,'cancelled')
            # Normal readiness/collection stays frozen and does not alter inventory again.
            receipt3,_=new_order(); oid3=int(receipt3.rsplit('/',1)[-1]); _,page=get('/manager'+receipt3)
            mark=next(key for key in page.forms if key.startswith('mark-picked-'));_,page=submit(page,mark)
            _,page=submit(page,'advance-order'); frozen=query('SELECT final_total FROM orders WHERE id=?',(oid3,))[0][0]
            assert 'order-cancel' not in page.forms and not any(key.startswith('mark-picked-') for key in page.forms)
            _,page=submit(page,'advance-order')
            assert query('SELECT status,final_total FROM orders WHERE id=?',(oid3,))[0]==('Completed',frozen) and stock(1)==initial[1]-2
            # Manager basket updates need no note; search is bounded and excludes existing lines.
            sid=next(cookie.value for cookie in jar if cookie.name=='shop_session')
            bid=query('SELECT id FROM baskets WHERE owner_session_id=? AND synthetic=0',(sid,))[0][0]
            _,page=get('/manager/baskets/'+bid)
            page=search(page,'basket-add-search-button','add-basket-item','whole milk',4,True)
            _,page=submit(page,'add-basket-item',{'product_id':'4','quantity':'1','reason':''},True)
            _,page=submit(page,'save-basket-item-4',{'quantity':'2','reason':''},True)
            assert stock(4)==initial[4]-2 and query('SELECT quantity FROM cart WHERE basket_id=? AND product_id=4',(bid,))[0][0]==2
            _,page=submit(page,'save-basket-item-4',{'quantity':'0','reason':'ok'})
            assert stock(4)==initial[4] and not query('SELECT * FROM cart WHERE basket_id=?',(bid,))
            # Reproduce stale CSRF via real logout/login, with no automatic mutation replay.
            _,page=get('/');_,cart=submit(page,'add-1',{'quantity':'1','return':'cart'})
            stale=copy.deepcopy(cart.forms['update-1']);post('/manager/logout',{'csrf':cart.hidden['csrf']})
            _,login=get('/manager/login');post('/manager/login',{'csrf':login.hidden['csrf'],'password':'local-demo-only'})
            assert stale['action']=='/cart' and stale['fields']['product_id']=='1'
            before_orders=query('SELECT COUNT(*) FROM orders WHERE session_id=?',(sid,))[0][0]
            fields=dict(stale['fields'],**{'quantity-1':'2'});status,headers,_,_=raw_post(stale['action'],fields,True)
            assert status==403 and headers['X-Shop-Error']=='csrf-expired' and headers['X-Shop-CSRF']
            assert query('SELECT quantity FROM cart WHERE basket_id=? AND product_id=1',(bid,))[0][0]==1
            fields['csrf']=headers['X-Shop-CSRF'];post(stale['action'],fields,True)
            assert query('SELECT quantity FROM cart WHERE basket_id=? AND product_id=1',(bid,))[0][0]==2
            assert query('SELECT COUNT(*) FROM orders WHERE session_id=?',(sid,))[0][0]==before_orders
            assert placed_snapshot(oid)==original
            # Closing/reopening the process preserves the working and placed histories.
            proc.terminate();proc.wait(timeout=5)
            proc=subprocess.Popen([str(ROOT/'bin/shop')],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            for _ in range(100):
                try:
                    _,page=get(receipt)
                    break
                except OSError:
                    time.sleep(.05)
            else: raise AssertionError('restart did not recover')
            assert '33% shopped' in page.text and placed_snapshot(oid)==original
            print(json.dumps({'result':'passed','checks':['bounded search GET, radio choice, HTML/HTMX POST','optional blank/short routine notes','stock/receipt database assertions','automatic start and exact pick replay','partial/cancel/frozen collection outcomes','manager basket updates','same-session CSRF explicit retry','restart persistence','HTML ID/label/form structure']}))
        finally:
            proc.terminate();proc.wait(timeout=5)

if __name__=='__main__':
    main()
