#!/usr/bin/env python3
"""Exercise manager override forms with a disposable server and database."""
import json
import os
import socket
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request
from pathlib import Path
from http.cookiejar import CookieJar
from html.parser import HTMLParser
ROOT = Path(__file__).resolve().parent.parent

class Page(HTMLParser):

    def __init__(self, text):
        super().__init__()
        self.html = text
        self.problem = False
        self.forms = {}
        self.current = None
        self.select = None
        self.ids = set()
        self.refs = []
        self.errors = []
        self.text = ''
        self.feed(text)
        for ref in self.refs:
            if ref not in self.ids:
                self.errors.append('missing referenced id ' + ref)
        assert not self.errors, self.errors
        assert 'ZgotmplZ' not in text

    def handle_starttag(self, tag, pairs):
        a = dict(pairs)
        if 'notice' in a.get('class', '').split() and 'error' in a.get('class', '').split() and ('hidden' not in a):
            self.problem = True
        if 'id' in a:
            if a['id'] in self.ids:
                self.errors.append('duplicate ID ' + a['id'])
            self.ids.add(a['id'])
        if tag == 'label' and 'for' in a:
            self.refs.append(a['for'])
        for ref in ['aria-labelledby', 'aria-describedby']:
            if ref in a:
                self.refs += a[ref].split()
        if tag == 'form':
            if self.current is not None:
                self.errors.append('nested form')
            self.current = {'action': a.get('action'), 'fields': {}, 'buttons': []}
        if self.current is not None:
            if tag in ['input', 'textarea', 'select'] and a.get('name'):
                self.current['fields'][a['name']] = a.get('value', '')
            if tag == 'select':
                self.select = a.get('name')
            if tag == 'option' and self.select and ('selected' in a):
                self.current['fields'][self.select] = a.get('value', '')
            if tag == 'button' and 'id' in a:
                self.current['buttons'].append(a['id'])

    def handle_endtag(self, tag):
        if tag == 'select':
            self.select = None
        if tag == 'form' and self.current is not None:
            for b in self.current['buttons']:
                self.forms[b] = self.current
            self.current = None

    def handle_data(self, data):
        self.text += ' ' + data

def main():
    with tempfile.TemporaryDirectory(prefix='shop-override-html-') as d:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        env = dict(os.environ, APP_ADDR=f'127.0.0.1:{port}', APP_ORIGIN=origin, DATABASE_PATH=d + '/shop.db', MANAGER_PASSWORD='local-demo-only')
        proc = subprocess.Popen([str(ROOT / 'bin/shop')], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            jar = CookieJar()
            client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

            def get(path, hx=False):
                req = urllib.request.Request(origin + path, headers={'HX-Request': 'true'} if hx else {})
                with client.open(req) as r:
                    return (r.geturl().replace(origin, ''), Page(r.read().decode()))

            def post(path, fields, hx=False, expect_problem=False):
                headers = {'Origin': origin}
                if hx:
                    headers['HX-Request'] = 'true'
                req = urllib.request.Request(origin + path, data=urllib.parse.urlencode(fields).encode(), headers=headers)
                with client.open(req) as r:
                    page = Page(r.read().decode())
                    assert page.problem == expect_problem, page.text
                    return (r.geturl().replace(origin, ''), page)

            def submit(page, button, changes=None, hx=False, expect_problem=False):
                f = page.forms[button]
                return post(f['action'], dict(f['fields'], **changes or {}), hx, expect_problem)
            for _ in range(100):
                try:
                    urllib.request.urlopen(origin + '/healthz')
                    break
                except OSError:
                    time.sleep(0.05)

            def new_order():
                _, p = get('/')
                _, p = submit(p, 'add-1', {'quantity': '2', 'return': 'cart'})
                return submit(p, 'place-order')
            receipt, p = new_order()
            _, login = get('/manager/login')
            _, home = get('/')
            csrf = home.forms['add-1']['fields']['csrf']
            post('/manager/login', {'csrf': csrf, 'password': 'local-demo-only'})
            _, p = get('/manager' + receipt)
            assert 'WORKING PICK LIST' in p.text and 'ORIGINAL PLACED TOTAL' in p.text
            line = next((k.removeprefix('order-set-') for k in p.forms if k.startswith('order-set-')))
            keys = [f['fields'].get('command_key') for f in p.forms.values() if 'command_key' in f['fields']]
            assert len(keys) == len(set(keys))
            _, p = submit(p, 'order-set-' + line, {'quantity': '3', 'reason': 'xx', 'disposition': 'writeoff'}, True, True)
            rejected = p.forms['order-set-' + line]['fields']
            assert rejected['quantity'] == '3' and rejected['reason'] == 'xx' and (rejected['disposition'] == 'writeoff')
            _, p = submit(p, 'order-set-' + line, {'quantity': '3', 'reason': 'Add extra demo apple', 'disposition': 'writeoff'})
            assert '3 requested' in p.text and '$6.98' in p.text
            _, p = submit(p, 'order-add-item', {'product_id': '3', 'quantity': '1', 'reason': 'Add demo bread', 'disposition': 'writeoff'}, True)
            assert 'Sourdough' in p.text
            _, p = submit(p, 'order-substitute-' + line, {'replacement_id': '1', 'quantity': '2', 'reason': 'Review replacement choice', 'disposition': 'restock'}, True, True)
            assert 'Previous choice (product 1) unavailable; choose again' in p.text
            assert p.forms['order-substitute-' + line]['fields']['reason'] == 'Review replacement choice'
            _, p = submit(p, 'order-substitute-' + line, {'replacement_id': '2', 'quantity': '2', 'reason': 'Substitute demo spinach', 'disposition': 'restock'})
            assert '2 requested' in p.text and 'Manager substitution' in p.text
            _, p = submit(p, 'advance-order')
            _, p = submit(p, 'save-picked-2', {'picked': '1'}, True)
            assert '33% shopped' in p.text and '1 / 3 units picked' in ' '.join(p.text.split())
            _, p = submit(p, 'order-finish-partial', {'remainder': 'unavailable', 'disposition': 'writeoff', 'reason': 'Unpicked demo products unavailable'})
            assert 'FINISHED PARTIALLY.' in p.text and 'order-cancel' not in p.forms
            assert '33% shopped' in p.text and 'Closed with missing items' in p.text
            _, p = get(receipt)
            assert 'Finished with only the picked items.' in p.text and 'unavailable' in p.text and ('FINAL TOTAL' in p.text)
            _, poll = get(receipt + '/status', True)
            assert '33% shopped' in poll.text and 'FINAL TOTAL' in poll.text and ('unavailable' in poll.text)
            receipt2, _ = new_order()
            _, p = get('/manager' + receipt2)
            _, p = submit(p, 'advance-order')
            _, p = submit(p, 'save-picked-1', {'picked': '1'})
            _, p = submit(p, 'order-cancel', {'disposition': 'restock', 'reason': 'Cancel entire demo order'})
            assert 'ORDER CANCELLED.' in p.text and '1 / 2 units picked' in ' '.join(p.text.split())
            _, p = get(receipt2)
            assert 'Picked counts and line amounts below are historical' in p.text and 'The final total is $0.00' in p.text
            receipt3, _ = new_order()
            _, p = get('/manager' + receipt3)
            _, p = submit(p, 'advance-order')
            _, p = submit(p, 'save-picked-1', {'picked': '2'})
            _, p = submit(p, 'advance-order')
            assert 'Its items and final total are frozen' in p.text and 'order-cancel' not in p.forms and ('save-picked-1' not in p.forms)
            assert 'order_version' in p.forms['advance-order']['fields']
            _, p = submit(p, 'advance-order')
            assert 'TICKET CLOSED.' in p.text and 'advance-order' not in p.forms
            print(json.dumps({'result': 'passed', 'checks': ['real HTTP add / absolute quantity change / substitute', 'per-form unique command keys, csrf and versions', 'rejected form draft preservation', 'stable-line pick forms', '1 of 3 finish retains 33 percent and unavailability', 'customer polling has working/final summary and history', 'picked-order cancellation retains history and final zero', 'Ready is frozen; collection completes', 'HTML duplicate IDs, label/ARIA references and form nesting']}))
        finally:
            proc.terminate()
            proc.wait(timeout=5)
if __name__ == '__main__':
    main()
