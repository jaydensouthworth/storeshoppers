# Product image normalization core

The dependency-free `internal/productimage` package implements the normalization boundary used by the manager image editor. It supports the application's Go 1.24 minimum; verification uses Go 1.27.1. See [Product images](PRODUCT_IMAGES.md) for the complete HTTP, storage and recovery workflow.

## Package contract

```go
result, err := productimage.Normalize(boundedMultipartImageReader)
// Or NormalizeBytes(raw), when the caller already has a bounded byte slice.
if err != nil {
    // Map errors.Is to safe, specific manager feedback; no partial output exists.
}
// Retain these exact bytes as the immutable preview and eventual stored asset.
// result.Master / result.Thumbnail each include Data, MIME, Width, Height.
// result.Version + result.SHA256 identify both normalized variants.

// Store/restore integrity verification, with no lossy re-encoding:
if err := productimage.ValidateResult(result); err != nil {
    // Refuse insertion/restoration of an inconsistent server-produced result.
}
```

`Normalize` consumes at most 4 MiB + 1 input bytes. `NormalizeBytes` enforces the same cap. Neither API accepts a filename, a claimed MIME type, a URL, or a filesystem path. A JPEG labelled `image/png` remains an actual JPEG source; a PNG labelled `image/jpeg` remains an actual PNG source; SVG/GIF/WebP/HEIC cannot gain acceptance through a claimed MIME. The containing HTTP workflow should not select the decoder from multipart metadata. Persist only server-generated output MIME `image/jpeg`.

`ValidateResult` checks fixed version/MIME, variant byte and edge limits, matching declared/decoded dimensions, complete baseline JPEG decoding, permitted JPEG segments without APP/COM metadata or post-EOI trailers, expected thumbnail dimensions, and recomputed SHA-256. It ignores informational `SourceFormat`, `SourceWidth`, and `SourceHeight`, so these do not have to be persisted. It validates integrity of trusted, server-produced variants; it does **not** prove normalization provenance, prevent a client from computing a matching hash, detect arbitrary padding hidden inside JPEG entropy data, or prove a thumbnail's pixel relationship to its master. Never use it as a replacement for normalizing client input.

## Defined limits and behavior

- JPEG and PNG only, chosen from bytes and decoded by those explicit standard-library decoders. Importing additional registered image decoders elsewhere cannot widen the accepted formats
- Maximum input 4,194,304 bytes, 4,096 pixels on either edge, and **4,000,000 total pixels**. DecodeConfig and division-based dimension bounds run before pixel decode
- JPEG marker preflight permits at most 32 scans before pixel decoding. Typical baseline/progressive JPEGs remain supported; unusually many-scan images are refused. This prevents repeated progressive scan amplification within the byte/pixel limits
- PNG preflight parses bounded chunk lengths and rejects actual `acTL`, `fcTL`, or `fdAT` chunks before IEND. Marker-like text inside metadata or discarded trailing data is not mistaken for animation. The PNG decoder verifies full image structure and CRCs
- The source is fully decoded, then only pixels are resampled and re-encoded. Source metadata, EXIF, ICC profiles, PNG text/ancillary chunks, and all trailing bytes are discarded. Originals are never returned or persisted by this package
- Master: at most 1,024 pixels on either edge and 524,288 bytes. Thumbnail: at most 256 pixels on either edge and 98,304 bytes. Both are complete baseline JPEGs with MIME `image/jpeg`
- Aspect ratio is retained to the nearest pixel; neither variant is enlarged. A very thin aspect ratio is clamped to one pixel on the short axis
- A deterministic integer area average reduces detail without nearest-neighbor aliasing. The thumbnail is generated from the uncompressed normalized master raster, not from an already compressed JPEG
- Transparency is deliberately composited onto white using premultiplied sRGB values. Output is opaque. JPEG quality attempts are 88, 82, 76, 70, then 64. If the result still exceeds its cap, the complete request fails; no truncated output, silent further shrink, or lower quality is returned
- Content identity is SHA-256 over the domain `instore-shopper/product-image\x00` followed by three uint64-big-endian-length-prefixed parts: UTF-8 normalization version, master JPEG bytes, thumbnail JPEG bytes. The version is `product-image-v1`. Source format and dimensions are excluded, allowing identical normalized pixels/bytes with different discarded metadata to deduplicate
- Changing normalization behavior requires a version decision. Go encoder changes may alter bytes and therefore identities; this package does not promise byte identity across different Go encoder implementations. Previously stored versions must retain their exact bytes

## Known visual limitations

EXIF orientation is discarded and **not applied**. A phone image stored sideways with an orientation tag may therefore normalize sideways. ICC profiles, gamma tags, and other color-management metadata are discarded and not interpreted, so some images can shift color. Transparency changes to white. The lossy JPEG output is appropriate for product photos, but small text and line art can soften. The manager's preview must display these exact normalized variants before attachment; a browser preview of the original can show different orientation/colors/transparency.

There is no arbitrary URL fetching, path-based input, browser API, SVG sanitizer, remote image service, AI generation, or EXIF parser.

## Responsibilities outside the normalization package

This package is not the full upload security boundary. The application integration enforces manager authorization and CSRF, request and multipart limits (including overhead, part count, and field count), read deadlines, bounded normalization concurrency, and a bounded preview cache. A synchronous standard-library decode cannot be interrupted mid-call by a context deadline; do not start unbounded background decode goroutines or treat a timeout as having stopped their work. Apply the process concurrency bound before normalization.

Validate immutable server-produced bytes before storage. Use transactional, persistent global byte/count quotas and rate limits; exact image identity deduplication; versioned, replay-safe preview confirmation; attachment audit; and retained recovery assets. Bind a preview to the authorized manager/session and exact normalized bytes. Do not accept a client-submitted Result or hash as authority.

Store asset blobs and variants immutably with additive schema sequencing. Preserve existing bundled-illustration choices. Historical receipts without an image snapshot must not gain invented images. Demo reset must retain uploaded assets and global quota/rate state, and recovery backup size needs its own budget and integrity checks. HTTP image responses should use the stored fixed MIME and `X-Content-Type-Options: nosniff`; never serve the original or upload filename as active content.

## Verification

The unit suite covers accepted image formats and dimensions, no upscaling, exact input and pixel boundaries, pre-decode dimension bombs, malformed lengths and CRCs, APNG markers, 32/33 JPEG scan boundaries, entropy stuffing/restarts/marker-like metadata, metadata and trailing-data removal, deterministic identity, transparency, area resampling, output quality fallback and refusal without partial output, result ownership, and stored-result tampering.

`FuzzNormalize` seeds valid JPEG, PNG, progressive JPEG, dimension bombs and unsupported formats. A successful fuzz normalization must produce complete bounded JPEGs and pass `ValidateResult`.

The four-million-pixel PNG benchmark is a local measurement, not a worst-case memory or latency promise. Progressive, 16-bit, interlaced, or incompressible inputs have different costs. Run with the repository's actual Go toolchain and account for concurrency separately.

The integrated check script runs the package unit/race suite and the actual-process product-image smoke test. Fuzz seeds run as ordinary regression tests; a sustained fuzz campaign is a separate optional check.
