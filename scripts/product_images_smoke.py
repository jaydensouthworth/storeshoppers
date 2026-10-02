#!/usr/bin/env python3
"""Real multipart preview/confirmation/media/restart/reset on a temporary shop."""
from contextlib import closing
from http.cookiejar import CookieJar
from pathlib import Path
import os
import socket
import sqlite3
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import zlib

from order_overrides_smoke import Page
from product_details_smoke import DetailForms
from smoke import Fields

ROOT = Path(__file__).resolve().parent.parent


def demo_png():
    def chunk(kind, data):
        return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data))
    pixels = b''.join(b'\x00' + bytes([255, 210, 30]) * 32 for _ in range(24))
    return (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 32, 24, 8, 2, 0, 0, 0))
            + chunk(b'tEXt', b'Comment\x00Synthetic QA artwork metadata must disappear')
            + chunk(b'IDAT', zlib.compress(pixels)) + chunk(b'IEND', b'') + b'discarded trailer')


def main():
    with tempfile.TemporaryDirectory(prefix='shop-images-') as directory:
        binary = Path(directory) / 'shop'
        go = ROOT / '.tools/go/bin/go'
        env = dict(os.environ)
        env.setdefault('GOCACHE', '/tmp/instore-gocache')
        env.setdefault('GOPATH', '/tmp/instore-gopath')
        subprocess.run([str(go) if go.exists() else 'go', 'build', '-o', str(binary), './cmd/shop'], cwd=ROOT, env=env, check=True, timeout=120)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        path = Path(directory) / 'shop.db'
        env.update(APP_ADDR=f'127.0.0.1:{port}', APP_ORIGIN=origin, DATABASE_PATH=str(path), MANAGER_PASSWORD='password', DEMO_MODE='true')
        client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

        def request(route, fields=None, hx=False, expected=200, raw=None, content_type=None):
            headers = {'Origin': origin}
            if hx:
                headers['HX-Request'] = 'true'
            data = raw if raw is not None else (None if fields is None else urllib.parse.urlencode(fields).encode())
            if content_type:
                headers['Content-Type'] = content_type
            try:
                response = client.open(urllib.request.Request(origin + route, data=data, headers=headers), timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                body = response.read()
                assert response.code == expected, (route, response.code, body[:1500])
                if response.headers.get_content_type() == 'text/html':
                    Page(body.decode())
                return body, response.headers

        def start():
            p = subprocess.Popen([str(binary)], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            for _ in range(100):
                try:
                    assert request('/healthz')[0] == b'ok\n'
                    return p
                except OSError:
                    if p.poll() is not None:
                        raise AssertionError('image smoke app exited')
                    time.sleep(.05)
            p.terminate(); p.wait(timeout=5)
            raise AssertionError('image smoke app not ready')

        def form(markup, action):
            return DetailForms(markup.decode()).action(action)

        process = start()
        try:
            home, _ = request('/')
            request('/manager/login', {'csrf': Fields(home.decode()).fields['csrf'], 'password': 'password'})
            editor, _ = request('/manager/catalog/products/1/image?q=apple')
            upload_path = '/manager/catalog/products/1/image/preview'
            fields = form(editor, upload_path)
            fields.pop('image', None)
            boundary = 'synthetic-product-image-boundary'
            body = b''
            for key, value in fields.items():
                body += f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode()
            body += (f'--{boundary}\r\nContent-Disposition: form-data; name="image"; filename="qa-only.png"\r\nContent-Type: image/png\r\n\r\n'.encode()
                     + demo_png() + f'\r\n--{boundary}--\r\n'.encode())
            preview, _ = request(upload_path, hx=True, raw=body, content_type=f'multipart/form-data; boundary={boundary}')
            confirmation_path = '/manager/catalog/products/1/image/confirm'
            confirmation = form(preview, confirmation_path)
            assert b'Review your image' in preview
            with closing(sqlite3.connect(path)) as db:
                baseline = db.execute('SELECT id,stock,version,price_version FROM products ORDER BY id').fetchall()
                assert db.execute('SELECT COUNT(*) FROM product_images').fetchone()[0] == 0
            confirmation['confirm'] = 'public-demo-image'
            saved, _ = request(confirmation_path, confirmation, hx=True)
            assert b'Product image saved' in saved
            request(confirmation_path, confirmation)
            with closing(sqlite3.connect(path)) as db:
                image_hash, = db.execute('SELECT image_hash FROM products WHERE id=1').fetchone()
                master, thumb = db.execute('SELECT master,thumbnail FROM product_images').fetchone()
                assert db.execute('SELECT COUNT(*) FROM product_image_commands').fetchone()[0] == 1
                assert db.execute('SELECT id,stock,version,price_version FROM products ORDER BY id').fetchall() == baseline
                assert b'Synthetic QA artwork metadata' not in master and b'discarded trailer' not in master
                assert master.startswith(b'\xff\xd8') and master.endswith(b'\xff\xd9')
            media, headers = request(f'/media/products/{image_hash}/thumb.jpg')
            assert media == thumb and headers.get_content_type() == 'image/jpeg'
            assert headers['X-Content-Type-Options'] == 'nosniff' and not headers.get('Set-Cookie')
            detail, _ = request('/products/1')
            assert f'/media/products/{image_hash}/master.jpg'.encode() in detail
            process.terminate(); process.wait(timeout=5)
            process = start()
            assert request(f'/media/products/{image_hash}/master.jpg')[0] == master
            expired, _ = request(confirmation_path, confirmation, expected=409)
            assert b'preview expired' in expired
            editor, _ = request('/manager/catalog/products/1/image')
            restore_path = '/manager/catalog/products/1/image/illustration'
            request(restore_path, form(editor, restore_path))
            with closing(sqlite3.connect(path)) as db:
                assert db.execute('SELECT image_hash FROM products WHERE id=1').fetchone()[0] is None
                assert db.execute('SELECT COUNT(*) FROM product_images').fetchone()[0] == 1
                budget = db.execute('SELECT * FROM product_image_limits').fetchall()
            reset, _ = request('/manager/demo/reset')
            request('/manager/demo/reset', {'csrf': Fields(reset.decode()).fields['csrf'], 'confirm': 'reset-shared-demo'})
            with closing(sqlite3.connect(path)) as db:
                assert db.execute('SELECT master,thumbnail FROM product_images').fetchone() == (master, thumb)
                assert db.execute('SELECT * FROM product_image_limits').fetchall() == budget
                assert db.execute('SELECT COUNT(*) FROM products WHERE image_hash IS NOT NULL').fetchone()[0] == 0
            assert list(Path(str(path) + '.demo-backups').glob('*.sqlite3'))
            print('Product images HTTP smoke passed: real multipart preview/confirm, explicit public choice, replay, metadata stripping, fixed media, catalog context, unchanged commerce, restart recovery, illustration restore, reset archive and retained quota/artwork')
        finally:
            process.terminate(); process.wait(timeout=5)


if __name__ == '__main__':
    main()
