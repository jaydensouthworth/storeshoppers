package productimage

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand"
	"testing"
)

func TestNormalizeFormatsDimensionsAndDeterminism(t *testing.T) {
	for _, tc := range []struct {
		name, format                                    string
		width, height, masterW, masterH, thumbW, thumbH int
	}{
		{"landscape", "jpeg", 1800, 1200, 1024, 683, 256, 171},
		{"portrait", "png", 600, 1200, 512, 1024, 128, 256},
		{"small stays small", "png", 73, 49, 73, 49, 73, 49},
		{"one-pixel edge", "png", 1, 4096, 1, 1024, 1, 256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := encodeFixture(t, photoFixture(tc.width, tc.height), tc.format)
			got, err := Normalize(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != NormalizationVersion || got.SourceFormat != tc.format || got.SourceWidth != tc.width || got.SourceHeight != tc.height {
				t.Fatalf("unexpected metadata: version=%s, format=%s, dimensions=%dx%d", got.Version, got.SourceFormat, got.SourceWidth, got.SourceHeight)
			}
			assertVariant(t, got.Master, tc.masterW, tc.masterH, MaxMasterEdge, MaxMasterBytes)
			assertVariant(t, got.Thumbnail, tc.thumbW, tc.thumbH, MaxThumbnailEdge, MaxThumbnailBytes)
			digest, err := hex.DecodeString(got.SHA256)
			if err != nil || len(digest) != 32 {
				t.Fatalf("not a SHA-256 identity: %q", got.SHA256)
			}
			again, err := NormalizeBytes(raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.SHA256 != again.SHA256 || !bytes.Equal(got.Master.Data, again.Master.Data) || !bytes.Equal(got.Thumbnail.Data, again.Thumbnail.Data) {
				t.Fatal("same source did not yield deterministic variants and identity")
			}
		})
	}
}

func TestUnsupportedAndMalformedInputs(t *testing.T) {
	validPNG := encodeFixture(t, photoFixture(16, 16), "png")
	validJPEG := encodeFixture(t, photoFixture(16, 16), "jpeg")
	badCRC := bytes.Clone(validPNG)
	badCRC[len(badCRC)-1] ^= 1
	badLength := bytes.Clone(validPNG)
	binary.BigEndian.PutUint32(badLength[33:37], 0xffffffff)
	for _, tc := range []struct {
		name string
		raw  []byte
		want error
	}{
		{"empty", nil, ErrEmpty},
		{"SVG", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), ErrUnsupported},
		{"GIF", []byte("GIF89a"), ErrUnsupported},
		{"WebP", []byte("RIFF\x04\x00\x00\x00WEBP"), ErrUnsupported},
		{"HEIC", []byte("\x00\x00\x00\x18ftypheic"), ErrUnsupported},
		{"HTML prefix", append([]byte("<html>"), validJPEG...), ErrUnsupported},
		{"JPEG signature only", []byte{0xff, 0xd8}, ErrMalformed},
		{"JPEG truncated", validJPEG[:len(validJPEG)/2], ErrMalformed},
		{"PNG signature only", validPNG[:8], ErrMalformed},
		{"PNG no end chunk", validPNG[:len(validPNG)-12], ErrMalformed},
		{"PNG corrupt CRC", badCRC, ErrMalformed},
		{"PNG overflowing chunk", badLength, ErrMalformed},
		{"PNG end chunk with data", append(bytes.Clone(validPNG[:len(validPNG)-12]), pngChunk("IEND", []byte("x"))...), ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeBytes(tc.raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if len(got.Master.Data) != 0 || len(got.Thumbnail.Data) != 0 || got.SHA256 != "" {
				t.Fatal("failure exposed partial output")
			}
		})
	}
}

func TestDimensionsCheckedBeforePixelDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h uint32
		want error
	}{
		{"wide", 4097, 1, ErrDimensions},
		{"tall", 1, 4097, ErrDimensions},
		{"pixel bomb", 4096, 4096, ErrDimensions},
		{"one above pixel limit", 2000, 2001, ErrDimensions},
		{"huge width", 0x7fffffff, 1, ErrDimensions},
		{"zero width", 0, 1, ErrMalformed},
		{"zero height", 1, 0, ErrMalformed},
		{"valid dimensions but no pixels", 2000, 2000, ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// This is a valid IHDR with no IDAT. ErrDimensions proves the
			// oversize source was rejected before pixel decompression was tried.
			_, err := NormalizeBytes(pngHeaderOnly(tc.w, tc.h))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	jpegBomb := encodeFixture(t, photoFixture(16, 16), "jpeg")
	sof := bytes.Index(jpegBomb, []byte{0xff, 0xc0})
	if sof < 0 {
		t.Fatal("fixture has no SOF")
	}
	binary.BigEndian.PutUint16(jpegBomb[sof+5:sof+7], 4096)
	binary.BigEndian.PutUint16(jpegBomb[sof+7:sof+9], 4096)
	if _, err := NormalizeBytes(jpegBomb); !errors.Is(err, ErrDimensions) {
		t.Fatalf("JPEG dimensions error = %v", err)
	}
}

func TestMaximumPixelBoundary(t *testing.T) {
	raw := encodeFixture(t, image.NewGray(image.Rect(0, 0, 2000, 2000)), "png")
	got, err := NormalizeBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	assertVariant(t, got.Master, 1024, 1024, MaxMasterEdge, MaxMasterBytes)
	assertVariant(t, got.Thumbnail, 256, 256, MaxThumbnailEdge, MaxThumbnailBytes)
}

func TestRejectAPNGChunksAnywhereBeforeIEND(t *testing.T) {
	base := encodeFixture(t, photoFixture(16, 16), "png")
	for _, kind := range []string{"acTL", "fcTL", "fdAT"} {
		for _, offset := range []int{33, len(base) - 12} {
			raw := insert(base, offset, pngChunk(kind, []byte{0, 0, 0, 1, 0, 0, 0, 0}))
			if _, err := NormalizeBytes(raw); !errors.Is(err, ErrAnimation) {
				t.Fatalf("%s at %d: error = %v", kind, offset, err)
			}
		}
	}
	// An incidental string inside metadata or after IEND is not an APNG chunk.
	raw := insert(base, 33, pngChunk("tEXt", []byte("Comment\x00acTL fcTL fdAT")))
	raw = append(raw, pngChunk("acTL", make([]byte, 8))...)
	if _, err := NormalizeBytes(raw); err != nil {
		t.Fatalf("false animation match: %v", err)
	}
}

func TestMetadataAndTrailingBytesAreDiscarded(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			base := encodeFixture(t, photoFixture(45, 37), format)
			secret := []byte("EXIF_GPS_AND_COMMENT_PRIVATE_PAYLOAD")
			var decorated []byte
			if format == "jpeg" {
				payload := append([]byte("Exif\x00\x00"), secret...)
				segment := []byte{0xff, 0xe1, 0, 0}
				binary.BigEndian.PutUint16(segment[2:4], uint16(len(payload)+2))
				decorated = insert(base, 2, append(segment, payload...))
			} else {
				decorated = insert(base, 33, pngChunk("tEXt", append([]byte("Comment\x00"), secret...)))
			}
			decorated = append(decorated, []byte("<script>trailing payload</script>")...)
			plain, err := NormalizeBytes(base)
			if err != nil {
				t.Fatal(err)
			}
			got, err := NormalizeBytes(decorated)
			if err != nil {
				t.Fatal(err)
			}
			if got.SHA256 != plain.SHA256 || !bytes.Equal(got.Master.Data, plain.Master.Data) || !bytes.Equal(got.Thumbnail.Data, plain.Thumbnail.Data) {
				t.Fatal("metadata or trailer influenced normalized content")
			}
			for _, variant := range []Variant{got.Master, got.Thumbnail} {
				if bytes.Contains(variant.Data, secret) || bytes.Contains(variant.Data, []byte("<script>")) {
					t.Fatal("source metadata or trailer escaped")
				}
				if !bytes.HasSuffix(variant.Data, []byte{0xff, 0xd9}) {
					t.Fatal("output has data past JPEG EOI")
				}
			}
		})
	}
}

func TestJPEGScanComplexityAndMarkers(t *testing.T) {
	for _, scans := range []int{1, MaxJPEGScans, MaxJPEGScans + 1, 1000} {
		raw := progressiveFixture(scans)
		result, err := NormalizeBytes(raw)
		if scans > MaxJPEGScans {
			if !errors.Is(err, ErrComplexity) {
				t.Fatalf("%d scans: error = %v", scans, err)
			}
		} else {
			if err != nil {
				t.Fatalf("%d scans: %v", scans, err)
			}
			assertVariant(t, result.Master, 8, 8, MaxMasterEdge, MaxMasterBytes)
		}
	}
	// Marker-like bytes inside APP metadata and escaped scan data must not
	// count as scans. This structurally framed fixture exercises the preflight;
	// pixel validity is independently checked by jpeg.Decode in the public API.
	raw := []byte{0xff, 0xd8}
	raw = append(raw, jpegSegment(0xe1, bytes.Repeat([]byte{0xff, 0xda}, 100))...)
	raw = append(raw, jpegSegment(0xda, []byte{1, 1, 0, 0, 63, 0})...)
	raw = append(raw, []byte{0x12, 0xff, 0x00, 0xda, 0xff, 0xd0, 0x34, 0xff, 0xff, 0xd7, 0x56, 0xff, 0xd9}...)
	if err := validateJPEGMarkers(raw, false); err != nil {
		t.Fatalf("metadata/stuffing/restart/fill: %v", err)
	}
	for _, malformed := range [][]byte{
		{0xff, 0xd8, 0xff, 0xe1, 0, 1, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff},
		{0xff, 0xd8, 0xff, 0xd8},
		{0xff, 0xd8, 0x01, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xd9},
	} {
		if err := validateJPEGMarkers(malformed, false); !errors.Is(err, ErrMalformed) {
			t.Fatalf("bad marker stream %x: %v", malformed, err)
		}
	}
}

func TestValidateResultWithoutReencoding(t *testing.T) {
	original, err := NormalizeBytes(encodeFixture(t, photoFixture(400, 300), "png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateResult(original); err != nil {
		t.Fatal(err)
	}
	withoutSource := original
	withoutSource.SourceFormat, withoutSource.SourceWidth, withoutSource.SourceHeight = "", 0, 0
	if err := ValidateResult(withoutSource); err != nil {
		t.Fatalf("source metadata was required for stored result: %v", err)
	}
	for _, tc := range []struct {
		name  string
		alter func(*Result)
	}{
		{"version", func(r *Result) { r.Version = "unknown" }},
		{"missing identity", func(r *Result) { r.SHA256 = "" }},
		{"wrong identity", func(r *Result) { r.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000" }},
		{"wrong MIME", func(r *Result) { r.Master.MIME = "image/png" }},
		{"wrong dimensions", func(r *Result) { r.Master.Width++ }},
		{"empty thumbnail", func(r *Result) { r.Thumbnail.Data = nil }},
		{"oversized master", func(r *Result) { r.Master.Data = make([]byte, MaxMasterBytes+1) }},
		{"metadata with matching hash", func(r *Result) {
			r.Master.Data = insert(r.Master.Data, 2, jpegSegment(0xe1, []byte("Exif\x00\x00private")))
			r.SHA256 = contentIdentity(r.Master.Data, r.Thumbnail.Data)
		}},
		{"trailer with matching hash", func(r *Result) {
			r.Master.Data = append(bytes.Clone(r.Master.Data), []byte("<script/>")...)
			r.SHA256 = contentIdentity(r.Master.Data, r.Thumbnail.Data)
		}},
		{"truncated with matching hash", func(r *Result) {
			r.Master.Data = r.Master.Data[:len(r.Master.Data)-3]
			r.SHA256 = contentIdentity(r.Master.Data, r.Thumbnail.Data)
		}},
		{"wrong thumbnail shape with matching hash", func(r *Result) {
			var err error
			r.Thumbnail, err = encodeJPEG(photoFixture(32, 16), MaxThumbnailBytes)
			if err != nil {
				t.Fatal(err)
			}
			r.SHA256 = contentIdentity(r.Master.Data, r.Thumbnail.Data)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := original
			tc.alter(&changed)
			if err := ValidateResult(changed); !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("tampered result accepted: %v", err)
			}
		})
	}
	if err := ValidateResult(original); err != nil {
		t.Fatalf("validation or tamper cases changed original bytes: %v", err)
	}
}

func TestInputReaderLimitAndReadErrors(t *testing.T) {
	reader := &countingReader{}
	if _, err := Normalize(reader); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("error = %v", err)
	}
	if reader.n != MaxInputBytes+1 {
		t.Fatalf("read %d bytes, want %d", reader.n, MaxInputBytes+1)
	}
	if _, err := NormalizeBytes(make([]byte, MaxInputBytes+1)); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("bytes error = %v", err)
	}
	if _, err := Normalize(nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("nil reader error = %v", err)
	}
	want := errors.New("broken upload")
	if _, err := Normalize(errorReader{want}); !errors.Is(err, want) {
		t.Fatalf("read error = %v", err)
	}

	// Exactly four MiB remains accepted when the excess is trailing data.
	raw := encodeFixture(t, photoFixture(16, 16), "png")
	raw = append(raw, make([]byte, MaxInputBytes-len(raw))...)
	if _, err := Normalize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("exact input cap: %v", err)
	}
}

func TestTransparencyAndAreaResampling(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	source.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 10, B: 40, A: 0})
	source.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 128})
	source.SetNRGBA(2, 0, color.NRGBA{R: 0, G: 0, B: 255, A: 255})
	got := resizeOpaque(source, 1024)
	for x, want := range []color.RGBA{{255, 255, 255, 255}, {255, 127, 127, 255}, {0, 0, 255, 255}} {
		if value := got.RGBAAt(x, 0); value != want {
			t.Fatalf("pixel %d = %#v, want %#v", x, value, want)
		}
	}
	checker := image.NewGray(image.Rect(7, 9, 9, 11))
	checker.SetGray(7, 9, color.Gray{255})
	checker.SetGray(8, 10, color.Gray{255})
	if value := resizeOpaque(checker, 1).RGBAAt(0, 0); value != (color.RGBA{128, 128, 128, 255}) {
		t.Fatalf("area average = %#v", value)
	}
	transparent := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	result, err := NormalizeBytes(encodeFixture(t, transparent, "png"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(result.Master.Data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(16, 16).RGBA()
	if r < 65000 || g < 65000 || b < 65000 {
		t.Fatalf("transparent upload did not become white: %d %d %d", r, g, b)
	}
}

func TestOutputLimitDoesNotReturnTruncatedData(t *testing.T) {
	noise := image.NewRGBA(image.Rect(0, 0, 900, 900))
	random := rand.New(rand.NewSource(8))
	for i := 0; i < len(noise.Pix); i += 4 {
		noise.Pix[i], noise.Pix[i+1], noise.Pix[i+2], noise.Pix[i+3] = uint8(random.Intn(256)), uint8(random.Intn(256)), uint8(random.Intn(256)), 255
	}
	var highQuality bytes.Buffer
	if err := jpeg.Encode(&highQuality, noise, &jpeg.Options{Quality: 88}); err != nil {
		t.Fatal(err)
	}
	if highQuality.Len() <= MaxMasterBytes {
		t.Fatalf("noise fixture did not require quality fallback: %d", highQuality.Len())
	}
	got, err := encodeJPEG(noise, MaxMasterBytes)
	if err != nil {
		t.Fatal(err)
	}
	assertVariant(t, got, 900, 900, MaxMasterEdge, MaxMasterBytes)
	failed, err := encodeJPEG(noise, 512)
	if !errors.Is(err, ErrOutputLimit) || len(failed.Data) != 0 {
		t.Fatalf("small cap returned data or wrong error: len=%d, err=%v", len(failed.Data), err)
	}
	buffer := cappedBuffer{limit: 5}
	if n, err := buffer.Write([]byte("12345")); n != 5 || err != nil {
		t.Fatalf("exact capped write: n=%d, err=%v", n, err)
	}
	if n, err := buffer.Write([]byte("6")); n != 0 || !errors.Is(err, ErrOutputLimit) || buffer.buffer.Len() != 5 {
		t.Fatalf("overflow write: n=%d, err=%v, len=%d", n, err, buffer.buffer.Len())
	}
	// At the maximum resolution, deliberately incompressible color noise does
	// not fit even at the quality floor. The public API must refuse it cleanly.
	larger := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for i := 0; i < len(larger.Pix); i += 4 {
		larger.Pix[i], larger.Pix[i+1], larger.Pix[i+2], larger.Pix[i+3] = uint8(random.Intn(256)), uint8(random.Intn(256)), uint8(random.Intn(256)), 255
	}
	result, err := NormalizeBytes(encodeFixture(t, larger, "png"))
	if !errors.Is(err, ErrOutputLimit) || len(result.Master.Data) != 0 || len(result.Thumbnail.Data) != 0 {
		t.Fatalf("incompressible input returned partial output or wrong error: %v", err)
	}
}

func TestPixelChangesHaveDistinctIdentityAndOutputOwnership(t *testing.T) {
	black := image.NewGray(image.Rect(0, 0, 16, 16))
	white := image.NewGray(image.Rect(0, 0, 16, 16))
	for i := range white.Pix {
		white.Pix[i] = 255
	}
	first, err := NormalizeBytes(encodeFixture(t, black, "png"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeBytes(encodeFixture(t, white, "png"))
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 == second.SHA256 {
		t.Fatal("different normalized pixels shared identity")
	}
	if contentIdentity(first.Master.Data, second.Thumbnail.Data) == first.SHA256 {
		t.Fatal("identity omitted thumbnail")
	}
	thumbnailBefore := bytes.Clone(first.Thumbnail.Data)
	first.Master.Data[0] ^= 1
	if !bytes.Equal(first.Thumbnail.Data, thumbnailBefore) {
		t.Fatal("variant backing storage is aliased")
	}
}

func FuzzNormalize(f *testing.F) {
	f.Add(encodeFixture(f, photoFixture(8, 8), "jpeg"))
	f.Add(encodeFixture(f, photoFixture(8, 8), "png"))
	f.Add(pngHeaderOnly(4096, 4096))
	f.Add(progressiveFixture(MaxJPEGScans))
	f.Add(progressiveFixture(MaxJPEGScans + 1))
	f.Add([]byte("GIF89a"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		result, err := NormalizeBytes(raw)
		if err != nil {
			if len(result.Master.Data) != 0 || len(result.Thumbnail.Data) != 0 {
				t.Fatal("error exposed output")
			}
			return
		}
		if result.SourceFormat != "jpeg" && result.SourceFormat != "png" {
			t.Fatalf("widened allowlist: %q", result.SourceFormat)
		}
		assertVariant(t, result.Master, result.Master.Width, result.Master.Height, MaxMasterEdge, MaxMasterBytes)
		assertVariant(t, result.Thumbnail, result.Thumbnail.Width, result.Thumbnail.Height, MaxThumbnailEdge, MaxThumbnailBytes)
		if err := ValidateResult(result); err != nil {
			t.Fatalf("normalized result did not validate: %v", err)
		}
	})
}

func BenchmarkNormalizeFourMegapixelPNG(b *testing.B) {
	raw := encodeFixture(b, photoFixture(2000, 2000), "png")
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NormalizeBytes(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func assertVariant(t testing.TB, variant Variant, width, height, edge, limit int) {
	t.Helper()
	if variant.MIME != "image/jpeg" || variant.Width != width || variant.Height != height || width < 1 || height < 1 || width > edge || height > edge || len(variant.Data) == 0 || len(variant.Data) > limit {
		t.Fatalf("invalid variant: MIME=%q, dimensions=%dx%d, bytes=%d", variant.MIME, variant.Width, variant.Height, len(variant.Data))
	}
	decoded, err := jpeg.Decode(bytes.NewReader(variant.Data))
	if err != nil {
		t.Fatalf("output is not a complete JPEG: %v", err)
	}
	if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
		t.Fatalf("output dimensions disagree with metadata: %v", decoded.Bounds())
	}
}

func photoFixture(width, height int) image.Image {
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			result.SetRGBA(x, y, color.RGBA{uint8(x * 255 / max(width-1, 1)), uint8(y * 255 / max(height-1, 1)), uint8((x+y)%128 + 64), 255})
		}
	}
	return result
}

func encodeFixture(t testing.TB, source image.Image, format string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&buffer, source, &jpeg.Options{Quality: 92})
	} else {
		err = png.Encode(&buffer, source)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func pngChunk(kind string, data []byte) []byte {
	chunk := make([]byte, 12+len(data))
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(data)))
	copy(chunk[4:8], kind)
	copy(chunk[8:], data)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	return chunk
}

func jpegSegment(marker byte, data []byte) []byte {
	segment := []byte{0xff, marker, 0, 0}
	binary.BigEndian.PutUint16(segment[2:4], uint16(len(data)+2))
	return append(segment, data...)
}

func progressiveFixture(scans int) []byte {
	// A tiny grayscale progressive JPEG with a DC scan followed by repeat AC
	// scans. Go permits duplicate AC scans, so a separate complexity cap matters.
	raw := []byte{0xff, 0xd8}
	raw = append(raw, jpegSegment(0xdb, append([]byte{0}, bytes.Repeat([]byte{1}, 64)...))...)
	raw = append(raw, jpegSegment(0xc2, []byte{8, 0, 8, 0, 8, 1, 1, 0x11, 0})...)
	huffman := []byte{0, 1}
	huffman = append(huffman, make([]byte, 15)...)
	huffman = append(huffman, 0, 0x10, 1)
	huffman = append(huffman, make([]byte, 15)...)
	huffman = append(huffman, 0)
	raw = append(raw, jpegSegment(0xc4, huffman)...)
	raw = append(raw, jpegSegment(0xda, []byte{1, 1, 0, 0, 0, 0})...)
	raw = append(raw, 0x7f)
	for i := 1; i < scans; i++ {
		raw = append(raw, jpegSegment(0xda, []byte{1, 1, 0, 1, 63, 0})...)
		raw = append(raw, 0x7f)
	}
	return append(raw, 0xff, 0xd9)
}

func pngHeaderOnly(width, height uint32) []byte {
	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header[:4], width)
	binary.BigEndian.PutUint32(header[4:8], height)
	header[8], header[9] = 8, 2
	raw := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", header)...)
	return append(raw, pngChunk("IEND", nil)...)
}

func insert(raw []byte, offset int, addition []byte) []byte {
	result := make([]byte, 0, len(raw)+len(addition))
	result = append(result, raw[:offset]...)
	result = append(result, addition...)
	return append(result, raw[offset:]...)
}

type countingReader struct{ n int }

func (r *countingReader) Read(p []byte) (int, error) { r.n += len(p); return len(p), nil }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

var _ io.Reader = (*countingReader)(nil)
