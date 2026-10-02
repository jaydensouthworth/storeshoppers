# Phone-first in-store demo

## Two-screen journey

1. On the desktop storefront, make a fake basket and place an order. Open **Employees**, assign a simulated shopper and choose **Connect shopper phone**.
2. Create a pairing code. Scan the one-time QR link with the phone's ordinary camera or enter the displayed code at `/handheld/`. The phone must explicitly tap **Connect this phone**. Issuing another code revokes the earlier connection.
3. Open a line in the phone pick list. Open that product's page on the desktop and display its **Demo scan label**. The PNG contains the exact stored product identity in Code 128; it is not a retail UPC/GTIN claim.
4. Tap **Start camera** on the phone, choose a local barcode photo, or enter the code manually. Recognition fills a draft only. Tap **Review code**, check the product and saved quantity, then explicitly **Save picked quantity**. The input is the absolute total picked, not an increment.
5. Manager and customer views read the same saved progress. The manager reviews the completed order and marks it Ready. Ready, reassignment, task cancellation, owner expiry and explicit revocation end this phone's authority.

The storefront has a clearly marked **In-store shopping demo** entry. The phone has its own shell, large touch controls, list/item views, appearance preference, plain-HTML form fallback and a way back to the storefront. It does not inherit the customer's basket or manager session.

## Authorization and scope

Schema 14 adds short-lived, hash-only pairing invitations and assignment-scoped grants. A code has 128 bits of entropy and expires after ten minutes. Redemption is single use. A grant lasts at most eight hours and never longer than the owning visitor session. The bootstrap cookie lasts thirty minutes. Redemption has bounded per-bootstrap and global attempt windows and a cap on active bootstraps.

The pairing QR uses a URL fragment, so its secret is not sent in request paths, referrers or access logs. The phone clears the fragment before doing work and never redeems it automatically. A new pairing link opened while connected does not silently switch jobs: it clears the fragment, pauses scanning and asks the user to disconnect and reopen the link. Pairing pages and phone responses are private/no-store. Pairing is a fake-data task capability, not an employee account or production credential model.

Cookies are HttpOnly, SameSite Strict, HTTPS Secure and scoped to `/handheld/`. Public HTTP is refused; a loopback exception supports local development. Every read and transaction checks the grant, current demo epoch, owner expiry, order state, assignment identity/version and shopper archive state. Mutating requests also require CSRF, origin, bounded form input and current item/order versions. The grant cannot open other orders, edit customer baskets, invoke manager routes or expose private manager notes. Manager holds have only a customer-safe explanation on the phone; existing permitted picking remains usable.

Assigning another shopper, cancelling the assignment, closing the order, issuing a new invitation or revoking the connection invalidates previous grants. Going On break or Off shift stops new assignments but intentionally does not abandon work already assigned. Database reopen preserves a still-valid connection; demo reset invalidates it with the rest of the visitor state.

## Picking and interruption

Recognition verifies the exact current product identity and selected working line. Unknown, wrong, ambiguous, archived, stale or foreign codes cannot record picks. Leading zeroes are retained. Scan sources and symbology are recorded with real successful picks, while old manager events remain honestly unrecorded.

Counted picking shares the manager's transactional service. It checks eligibility and versions before replay, uses an explicit command key and writes the absolute count. Repeated exact confirmation cannot double-record a pick or deduct stock twice. The original placed receipt and working allocation remain unchanged. A changed payload cannot reuse a consumed key. Explicit re-review reads current state and supplies a fresh command key before another deliberate confirmation.

Offline/network failures retain the visible draft without automatic replay. Controls lock only while the submitted request is pending. Older HTMX responses, including redirect headers, cannot replace a newer navigation intent. Invalid counts remain editable; stale conflicts preserve the unsaved count for explicit code re-review. Grant expiry/revocation locks further writes and stops the camera. Back/Forward and reconnect do not automatically resubmit a command.

Weighted lines are visible and their identities can be checked, but actual grams still require the manager's existing reviewed measurement flow. The label identifies the product, never a package weight. Substitution/unavailable actions, structured customer approvals, phone weight review and phone completion/staging are the next bounded increments, not silently implied features.

## Camera and photo privacy

Camera access starts only on an explicit tap. Native `BarcodeDetector` is used only when Code 128 is supported. Otherwise the exact locally bundled ZXing decoder reads browser pixels. The camera stream stops on a successful decode, stop, page hide/navigation, document hidden, component removal, revocation or a two-minute timeout. Late permission promises and decode callbacks cannot restart a stopped camera or affect a replaced task.

Photos are decoded locally with strict byte/pixel bounds. There is no photo-upload endpoint, external decoder, analytics call or runtime CDN. A code fills the input; it never submits the form or records a pick. Permission denial, no camera, unknown results and unreadable photos leave manual entry usable. See [dependency provenance and licenses](barcode-scanner-dependencies.md).

## Verification and remaining acceptance

The aggregate `scripts/check.sh` includes:

- Go domain/HTTP/migration/security/concurrency tests with race detection and vet
- Independent decoding of actual generated PNG pixels, using pinned ZXing for every seeded identity and additional long/catalog examples; rotation, JPEG, moderate blur/skew, low contrast and damaged/cropped negative fixtures
- Scanner lifecycle tests with controlled media promises, pixels, timers, image decoding and native-result stubs
- Handheld DOM/lifecycle tests covering stale responses and headers, offline drafts, expiry, Back/Forward, fragment cleanup, explicit clipboard copy and no browser storage of grants/codes
- A real-process smoke test using separate desktop, phone and attacker cookie jars; rendered pairing/scan/count forms; wrong/unknown/matching codes; replay; unchanged inventory/receipt; private-note isolation; actual process restart; persisted grant/progress; and revocation before replay

These are not evidence of physical camera optics. Live desktop/narrow-screen QA and Samsung S23 Ultra Chrome camera acceptance are separate release checks. The physical-device checklist is: allow/deny camera, rear lens and focus, desktop label at normal/large size, glare and low light, rotation, retry, app switch/lock/resume, photo import, wrong label, weighted label, manual fallback, reconnect/revocation and TalkBack. Never claim physical-device acceptance from a viewport or mocked camera test.

## Connected roadmap

The [project plan](https://chatgpt.com/space/page_6abd4f41ae448191a351b73099da8658) includes sourced workflow research and the detailed acceptance matrix. Next releases should preserve the same authorization/transaction boundaries:

1. Phone measured-weight review and explicit manager escalation for exceptions
2. Session-scoped customer/shopper message threads with separate message revisions, no external notifications or real personal data
3. Structured substitution preferences and proposal/approve/reject transitions; chat alone is never consent
4. Unavailable outcomes and explicit manager overrides with stock/audit invariants
5. Staging and pickup handoff with separate authority after Ready, because Ready currently closes the picking assignment
6. Batching only after each individual-order lifecycle is complete; no decorative productivity analytics
