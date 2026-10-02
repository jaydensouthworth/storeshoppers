// Package productimage validates and normalizes bounded, untrusted JPEG and PNG
// images. It does not fetch URLs, open files, persist assets, or enforce request
// concurrency or storage quotas. Callers must provide those integration limits.
package productimage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
)

const (
	MaxInputBytes     = 4 * 1024 * 1024
	MaxInputEdge      = 4096
	MaxInputPixels    = 4_000_000
	MaxJPEGScans      = 32
	MaxMasterEdge     = 1024
	MaxMasterBytes    = 512 * 1024
	MaxThumbnailEdge  = 256
	MaxThumbnailBytes = 96 * 1024

	// NormalizationVersion must change if pixel conversion, resampling, encoder
	// settings, size limits, or the identity framing change. A toolchain encoder
	// change can also change the bytes and thus the content identity.
	NormalizationVersion = "product-image-v1"
	OutputMIME           = "image/jpeg"
)

var (
	ErrEmpty         = errors.New("image input is empty")
	ErrInputLimit    = errors.New("image input exceeds the byte limit")
	ErrUnsupported   = errors.New("only JPEG and PNG images are supported")
	ErrMalformed     = errors.New("image is malformed")
	ErrDimensions    = errors.New("image dimensions exceed the limits")
	ErrAnimation     = errors.New("animated PNG images are not supported")
	ErrComplexity    = errors.New("JPEG image has too many scans")
	ErrOutputLimit   = errors.New("normalized image cannot fit the output limit")
	ErrInvalidResult = errors.New("normalized image result failed validation")
)

// Variant contains a complete JPEG, never a truncated encoder result. Data is
// owned by the caller, who must treat persisted bytes as immutable. MIME is set
// by this package and never comes from an upload header or filename.
type Variant struct {
	Data   []byte
	MIME   string
	Width  int
	Height int
}

// Result identifies both normalized variants with one length-framed SHA-256.
// SourceFormat is the actual decoded format ("jpeg" or "png"). Source dimensions
// are provided for review only; they are not included in content identity.
type Result struct {
	Version      string
	SHA256       string
	SourceFormat string
	SourceWidth  int
	SourceHeight int
	Master       Variant
	Thumbnail    Variant
}

// Normalize reads at most MaxInputBytes+1 bytes before enforcing the input cap.
// HTTP integrations must also cap the entire request, multipart overhead and
// read time. Neither a claimed MIME type nor a filename is accepted here.
func Normalize(r io.Reader) (Result, error) {
	if r == nil {
		return Result{}, ErrEmpty
	}
	raw, err := io.ReadAll(io.LimitReader(r, MaxInputBytes+1))
	if len(raw) > MaxInputBytes {
		return Result{}, ErrInputLimit
	}
	if err != nil {
		return Result{}, fmt.Errorf("read image: %w", err)
	}
	return NormalizeBytes(raw)
}

// NormalizeBytes normalizes an already bounded upload buffer. The caller must
// not mutate raw during the call. The returned result retains no input bytes.
func NormalizeBytes(raw []byte) (Result, error) {
	if len(raw) == 0 {
		return Result{}, ErrEmpty
	}
	if len(raw) > MaxInputBytes {
		return Result{}, ErrInputLimit
	}

	// Call the selected decoder directly. Other packages registering additional
	// image formats cannot widen this allowlist.
	var (
		format  string
		config  image.Config
		decoded image.Image
		err     error
	)
	switch {
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		format = "png"
		config, err = png.DecodeConfig(bytes.NewReader(raw))
	case len(raw) >= 2 && raw[0] == 0xff && raw[1] == 0xd8:
		format = "jpeg"
		config, err = jpeg.DecodeConfig(bytes.NewReader(raw))
	default:
		return Result{}, ErrUnsupported
	}
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if !validDimensions(config.Width, config.Height) {
		return Result{}, ErrDimensions
	}

	if format == "png" {
		// Go's PNG decoder accepts APNG as its static fallback image. Inspect
		// actual chunk boundaries so animation is rejected, but strings in image
		// data, text metadata, or trailing bytes are not false positives.
		if err := validatePNGChunks(raw); err != nil {
			return Result{}, err
		}
		decoded, err = png.Decode(bytes.NewReader(raw))
	} else {
		if err := validateJPEGMarkers(raw, false); err != nil {
			return Result{}, err
		}
		decoded, err = jpeg.Decode(bytes.NewReader(raw))
	}
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	bounds := decoded.Bounds()
	if !validDimensions(bounds.Dx(), bounds.Dy()) || bounds.Dx() != config.Width || bounds.Dy() != config.Height {
		return Result{}, ErrDimensions
	}

	// Flatten onto white in premultiplied sRGB channel space. This deliberately
	// does not interpret EXIF orientation or ICC profiles; the review must show
	// these exact normalized pixels before the manager attaches the image.
	masterRaster := resizeOpaque(decoded, MaxMasterEdge)
	master, err := encodeJPEG(masterRaster, MaxMasterBytes)
	if err != nil {
		return Result{}, fmt.Errorf("master: %w", err)
	}
	thumbnail, err := encodeJPEG(resizeOpaque(masterRaster, MaxThumbnailEdge), MaxThumbnailBytes)
	if err != nil {
		return Result{}, fmt.Errorf("thumbnail: %w", err)
	}
	return Result{
		Version:      NormalizationVersion,
		SHA256:       contentIdentity(master.Data, thumbnail.Data),
		SourceFormat: format,
		SourceWidth:  config.Width,
		SourceHeight: config.Height,
		Master:       master,
		Thumbnail:    thumbnail,
	}, nil
}

// ValidateResult verifies stored normalized variants and their framed identity
// without lossy re-encoding. It checks complete baseline JPEGs, no metadata or
// trailing bytes, fixed MIME/version, output bounds, thumbnail dimensions, and
// SHA-256. SourceFormat and SourceWidth/Height are informational and ignored.
// Callers must not mutate the result concurrently with validation or storage.
// This is an integrity check for server-produced results, not proof that bytes
// passed Normalize or that thumbnail pixels derive from the master. Never use it
// to accept client-supplied "normalized" variants instead of calling Normalize.
func ValidateResult(result Result) error {
	if result.Version != NormalizationVersion {
		return fmt.Errorf("%w: unsupported normalization version", ErrInvalidResult)
	}
	if err := validateVariant(result.Master, MaxMasterEdge, MaxMasterBytes); err != nil {
		return fmt.Errorf("%w: master: %v", ErrInvalidResult, err)
	}
	if err := validateVariant(result.Thumbnail, MaxThumbnailEdge, MaxThumbnailBytes); err != nil {
		return fmt.Errorf("%w: thumbnail: %v", ErrInvalidResult, err)
	}
	tw, th := fitDimensions(result.Master.Width, result.Master.Height, MaxThumbnailEdge)
	if result.Thumbnail.Width != tw || result.Thumbnail.Height != th {
		return fmt.Errorf("%w: inconsistent thumbnail dimensions", ErrInvalidResult)
	}
	if result.SHA256 != contentIdentity(result.Master.Data, result.Thumbnail.Data) {
		return fmt.Errorf("%w: content identity mismatch", ErrInvalidResult)
	}
	return nil
}

func validateVariant(variant Variant, edge, limit int) error {
	if variant.MIME != OutputMIME || len(variant.Data) == 0 || len(variant.Data) > limit || variant.Width < 1 || variant.Height < 1 || variant.Width > edge || variant.Height > edge {
		return errors.New("invalid MIME, dimensions, or byte count")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(variant.Data))
	if err != nil || config.Width != variant.Width || config.Height != variant.Height {
		return errors.New("JPEG dimensions do not match")
	}
	if err := validateJPEGMarkers(variant.Data, true); err != nil {
		return err
	}
	if _, err := jpeg.Decode(bytes.NewReader(variant.Data)); err != nil {
		return errors.New("incomplete or malformed JPEG")
	}
	return nil
}

func validateJPEGMarkers(raw []byte, normalized bool) error {
	if len(raw) < 2 || raw[0] != 0xff || raw[1] != 0xd8 {
		return fmt.Errorf("%w: missing JPEG signature", ErrMalformed)
	}
	scans, offset := 0, 2
	entropy := false
	for offset < len(raw) {
		if entropy {
			// Entropy data escapes FF as FF00; restart markers are also valid
			// within a scan. Segment payloads are skipped by their lengths below.
			next := bytes.IndexByte(raw[offset:], 0xff)
			if next < 0 {
				break
			}
			offset += next
		} else if raw[offset] != 0xff {
			return fmt.Errorf("%w: invalid JPEG marker", ErrMalformed)
		}
		for offset < len(raw) && raw[offset] == 0xff {
			offset++
		}
		if offset == len(raw) {
			break
		}
		marker := raw[offset]
		offset++
		if entropy && (marker == 0x00 || marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		entropy = false
		if marker == 0xd9 {
			if scans == 0 || normalized && (scans != 1 || offset != len(raw)) {
				return fmt.Errorf("%w: invalid JPEG end", ErrMalformed)
			}
			return nil
		}
		if marker == 0x00 || marker >= 0xd0 && marker <= 0xd8 {
			return fmt.Errorf("%w: unexpected JPEG marker", ErrMalformed)
		}
		if normalized && marker != 0xc0 && marker != 0xc4 && marker != 0xdb && marker != 0xda {
			return fmt.Errorf("%w: unexpected normalized JPEG segment", ErrMalformed)
		}
		if marker == 0x01 { // Standalone TEM marker; decoder decides support.
			continue
		}
		if len(raw)-offset < 2 {
			break
		}
		length := int(binary.BigEndian.Uint16(raw[offset : offset+2]))
		if length < 2 || length > len(raw)-offset {
			return fmt.Errorf("%w: invalid JPEG segment length", ErrMalformed)
		}
		offset += length
		if marker == 0xda {
			scans++
			if scans > MaxJPEGScans {
				return ErrComplexity
			}
			entropy = true
		}
	}
	return fmt.Errorf("%w: missing JPEG end", ErrMalformed)
}

func validDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= MaxInputEdge && height <= MaxInputEdge && width <= MaxInputPixels/height
}

func validatePNGChunks(raw []byte) error {
	// The signature and IHDR were already checked by DecodeConfig. uint64
	// comparisons ensure hostile uint32 lengths cannot wrap int on 32-bit Go.
	for offset := 8; offset <= len(raw); {
		if len(raw)-offset < 12 {
			return fmt.Errorf("%w: incomplete PNG chunk", ErrMalformed)
		}
		length := uint64(binary.BigEndian.Uint32(raw[offset : offset+4]))
		if length > uint64(len(raw)-offset-12) {
			return fmt.Errorf("%w: invalid PNG chunk length", ErrMalformed)
		}
		switch string(raw[offset+4 : offset+8]) {
		case "acTL", "fcTL", "fdAT":
			return ErrAnimation
		case "IEND":
			if length != 0 {
				return fmt.Errorf("%w: invalid PNG end chunk", ErrMalformed)
			}
			// Trailing data is intentionally discarded, never served or retained.
			return nil
		}
		offset += int(length) + 12
	}
	return fmt.Errorf("%w: missing PNG end chunk", ErrMalformed)
}

func fitDimensions(width, height, edge int) (int, int) {
	if width <= edge && height <= edge {
		return width, height
	}
	if width >= height {
		return edge, max(1, (height*edge+width/2)/width)
	}
	return max(1, (width*edge+height/2)/height), edge
}

// resizeOpaque uses an area average, so reducing detailed photos does not alias
// like nearest-neighbor sampling. Integer overlap weights make the resampler
// deterministic and avoid a new image dependency. It never enlarges an image.
func resizeOpaque(src image.Image, edge int) *image.RGBA {
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	dw, dh := fitDimensions(sw, sh, edge)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	denominator := uint64(sw) * uint64(sh)
	// Standard-library decoder image types implement RGBA64Image. Using its
	// concrete color return avoids a heap allocation for every sampled pixel.
	sample := func(x, y int) color.RGBA64 {
		r, g, b, a := src.At(x, y).RGBA()
		return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
	}
	if source, ok := src.(image.RGBA64Image); ok {
		sample = source.RGBA64At
	}
	for dy := 0; dy < dh; dy++ {
		y0, y1 := dy*sh, (dy+1)*sh
		for dx := 0; dx < dw; dx++ {
			x0, x1 := dx*sw, (dx+1)*sw
			var red, green, blue uint64
			for sy := y0 / dh; sy < (y1+dh-1)/dh; sy++ {
				wy := uint64(min(y1, (sy+1)*dh) - max(y0, sy*dh))
				for sx := x0 / dw; sx < (x1+dw-1)/dw; sx++ {
					wx := uint64(min(x1, (sx+1)*dw) - max(x0, sx*dw))
					pixel := sample(bounds.Min.X+sx, bounds.Min.Y+sy)
					weight := wx * wy
					white := uint64(0xffff - pixel.A)
					red += (uint64(pixel.R) + white) * weight
					green += (uint64(pixel.G) + white) * weight
					blue += (uint64(pixel.B) + white) * weight
				}
			}
			i := dy*dst.Stride + dx*4
			// Round the 16-bit average directly to 8 bits (65535 / 255 = 257).
			scale := denominator * 257
			dst.Pix[i+0] = uint8((red + scale/2) / scale)
			dst.Pix[i+1] = uint8((green + scale/2) / scale)
			dst.Pix[i+2] = uint8((blue + scale/2) / scale)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, ErrOutputLimit
	}
	return b.buffer.Write(p)
}

func encodeJPEG(raster image.Image, limit int) (Variant, error) {
	// Never silently reduce below this quality floor or truncate output. The
	// bounded writer aborts an attempt before its retained bytes exceed the cap.
	for _, quality := range [...]int{88, 82, 76, 70, 64} {
		output := cappedBuffer{limit: limit}
		err := jpeg.Encode(&output, raster, &jpeg.Options{Quality: quality})
		if errors.Is(err, ErrOutputLimit) {
			continue
		}
		if err != nil {
			return Variant{}, fmt.Errorf("encode image: %w", err)
		}
		bounds := raster.Bounds()
		return Variant{Data: output.buffer.Bytes(), MIME: OutputMIME, Width: bounds.Dx(), Height: bounds.Dy()}, nil
	}
	return Variant{}, ErrOutputLimit
}

func contentIdentity(master, thumbnail []byte) string {
	hash := sha256.New()
	hash.Write([]byte("instore-shopper/product-image\x00"))
	var size [8]byte
	for _, part := range [][]byte{[]byte(NormalizationVersion), master, thumbnail} {
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		hash.Write(size[:])
		hash.Write(part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
