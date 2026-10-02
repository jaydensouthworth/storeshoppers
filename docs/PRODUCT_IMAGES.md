# Product images

Products → open a product → **Manage product image** opens a dedicated editor.
Choose a JPEG or PNG, preview the exact normalized result, and explicitly confirm
that the demo artwork may appear on the public storefront. No catalog change is
made while previewing. The editor preserves bounded product-list filters in its
back link and forms. Normal HTML works; HTMX keeps invalid/expired-form feedback
beside the current image workflow without silently replaying a write.

The current photo is shown separately. **Use bundled illustration** restores the
product's selected illustration without deleting an upload. Product details have
their existing illustration chooser; it edits the fallback, not an attached photo.
Create a new product first, then attach its image. Archived products must be
restored before their image changes.

## Input and preview

- Static JPEG and PNG only, maximum 4 MiB, 4 million pixels and 4096px per edge
- Byte-based decoder selection; claimed file MIME and filenames never authorize a format
- Bounded PNG chunk checks and 32-scan JPEG preflight before full decode
- Re-encode pixels as opaque JPEG, stripping originals, filenames, EXIF/GPS,
  ICC profiles, text metadata and trailing bytes
- Master up to 1024px/512KiB; thumbnail up to 256px/96KiB; no upscaling
- White transparency; EXIF orientation and color profiles are not interpreted.
  Check the preview and rotate/correct the source file before uploading if needed

The request cap is 4 MiB+32KiB, with at most 16 parts, 64-byte field names,
4096-byte individual text fields and 8192 aggregate field/name bytes. The HTTP
server also has read deadlines. Existing manager authentication is checked before
reading a file; CSRF and current product version are checked before decoding.
Rejected POSTs do not mint replacement session cookies.

One upload/confirmation validation can decode per application process. Attempts
also use a persistent shared budget of 10 per minute and 50 per UTC day. This is a
bounded public demo, so another visitor may temporarily consume that allowance.
At most 8 normalized previews are held in memory for 10 minutes. Each is bound to
the existing manager session, product, catalog version and exact bytes. Private
preview responses are no-store. An app restart discards previews and clearly asks
the manager to choose the file again; previously saved images persist.

## Saved artwork and commerce

Schema 9 adds immutable `product_images`, replay records, global decode budgets
and a nullable current-product image association. Existing products receive no
invented image. Content identity hashes the normalization version and both JPEG
variants using length framing. Re-uploading identical normalized artwork reuses
the record. Stored-result validation checks integrity, not provenance; all client
uploads must still pass the full normalizer.

An immediate transaction checks catalog version, byte/count quota, deduplication,
association, audit and replay identity together. Maximum retained library is 128
assets or 32MiB across both variants. A stale update, full quota or failed audit
rolls back the entire save. Image edits change only catalog_version: stock,
reservations, price versions, order snapshots and totals are untouched. Historical
receipts and working lines stay text-only; current storefront, basket, catalog,
weekly/featured presentation and product details use the associated image.

Public media uses strict hash/variant routes, fixed `image/jpeg`, `nosniff`, ETags,
bounded lengths and immutable caching. It never creates session cookies. Uploaded
artwork is public after confirmation and remains at its content address even if
later detached. Use only nonpersonal demo artwork that can be shared. There is no
arbitrary URL fetch, SVG upload, original-file download, external image service or
asset deletion endpoint.

## Reset and recovery

Confirmed demo reset makes the normal consistent recovery snapshot. Before the
fresh baseline replaces the live database, it copies and validates the immutable
image library and persistent decode budgets. Seeded product associations replace
edited associations, but the old association remains in the recovery snapshot.
Reset does not replenish image storage or preview budgets and never silently
purges assets. Invalid retained variants or quota violations refuse reset while
preserving the original live data. The independent backup budget still applies.

## Verification

Tests cover input/variant bounds and metadata stripping, malformed/animated and
many-scan files, private previews, CSRF/session expiry, semaphore limits, stale
catalog updates, confirmation, replay, concurrent associations, deduplication,
quota, audit rollback, normal restart, reset retention/corruption refusal and
populated schema 8 upgrade/rollback/retry. The actual-process smoke exercises real
multipart HTML/HTMX forms, normalized image bytes and headers, public detail
bindings, unchanged commerce, restart recovery and confirmed local reset.

The new `X-Shop-Schema` dynamic-response header reports the serving binary's
supported schema for deployment diagnosis. It does not prove every old replica
has drained; weighted migration retains its separate compatibility acceptance gate.
