# Barcode and local scanner dependency provenance

Acquired 2026-10-02 through the official Go module proxy/checksum database and npm registry. No runtime CDN, network decoder or photo upload is used.

## Encoder

- Module: `github.com/boombuler/barcode` **v1.1.0**, pinned by `go.mod` and `go.sum`
- Source: https://github.com/boombuler/barcode/tree/v1.1.0
- Module proxy: https://proxy.golang.org/github.com/boombuler/barcode/@v/v1.1.0.zip
- License: MIT; reproduced unchanged as `web/static/BARCODE-LICENSE`
- Code 128 includes the encoder's checksum/stop symbols. Production adds 12 white modules at each side, 3 pixels per module and a 156px bar height within an opaque 180px PNG. No SVG script or font is involved
- Pairing QR uses the same module's QR encoder, M error correction, integer scale 6 and a four-module white border. Its only representation is an inline PNG in the authorized manager response. Pairing secrets must remain in URL fragments, never media URLs

## Pixel decoder

- Package: `@zxing/library` **0.23.0**
- Official tarball: https://registry.npmjs.org/@zxing/library/-/library-0.23.0.tgz
- Acquisition: `npm pack @zxing/library@0.23.0 --ignore-scripts --json`
- npm SHA-512 integrity: `sha512-6fkkoFwP8CHxl6ugnPsj74PLJgX2iRv5zczGAyt5OBzQgxFhuhF0NCEc4t4OvSr8xAv2MRLlI0Iu9ZGDZQ2urA==`
- Tarball SHA-256: `e49f1076d9af70179c9fbb5026cda67e48864f3bced64fff548237208cc2ff32`
- Exact unmodified `package/umd/index.min.js` copied to `web/static/zxing-library-0.23.0.min.js`
- JavaScript SHA-256: `3ede94153fb0c5b67a12d7adff6decd827c2b22714fdc6faecf27a8f20937ea6`
- License: full upstream Apache-2.0 license and bundled jai-imageio third-party notice copied unchanged to `web/static/ZXING-LICENSE`
- The package includes no separate NOTICE file. Upstream JS copyright notices are retained in `web/static/ZXING-NOTICES`
- Bundled dependency `ts-custom-error` is MIT. Its unchanged 3.3.1 license is `web/static/TS-CUSTOM-ERROR-LICENSE`, obtained from https://registry.npmjs.org/ts-custom-error/-/ts-custom-error-3.3.1.tgz
- Browser wrappers, including deprecated ZXing camera wrappers, are not used

## Verification

`go test ./internal/shop -run '^TestBarcode' -count=1 -v` independently decodes actual PNG-round-tripped pixels with pinned ZXing, for every seed plus new/catalog and long identity examples. The production decoder runs over those same pixels. Coverage includes 90/180/270° rotation, 2px modules, JPEG, moderate blur/skew, poor contrast, blank images, cropped starts/quiet zones, damaged checksum and multiple labels. Negative conditions never yield another product. The independent decode check requires Node; the aggregate script always invokes Node before Go tests.

`node --test scripts/scanner_test.cjs` uses controlled media promises, timers, canvas pixels and native-result stubs. It verifies explicit start, fallback availability, single decode, release on stop/hidden/navigation/removal/revocation, late permissions/results, retries, bounded photos and no submit on recognition.

These checks do not establish physical camera performance. Samsung S23 Ultra Chrome acceptance (actual camera permission, optics, glare, lighting, zoom, rotation, app switch/lock and TalkBack) remains a physical-device test.
