package shop

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestBarcodeUsesOnlyActiveStoredIdentity(t *testing.T) {
	s := newTestStore(t)
	testExec(t, s, `UPDATE products SET sku='CHANGED-SKU',barcode='FAKE-LEGACY' WHERE id=1`)
	testExec(t, s, `UPDATE product_codes SET raw_value='SHOPDEMO-STORED-IDENTITY',normalized_value='SHOPDEMO-STORED-IDENTITY' WHERE product_id=1 AND scheme='demo_local'`)
	label, err := s.ProductBarcode(1)
	if err != nil || label.Code != "SHOPDEMO-STORED-IDENTITY" {
		t.Fatalf("stored label = %+v %v", label, err)
	}
	for _, change := range []string{`UPDATE product_codes SET archived=1 WHERE product_id=1`, `UPDATE products SET archived=1 WHERE id=1`, `UPDATE product_codes SET symbology='EAN13' WHERE product_id=1 AND scheme='demo_local'`, `UPDATE product_codes SET raw_value='different' WHERE product_id=1 AND scheme='demo_local'`} {
		testExec(t, s, change)
		if _, err := s.ProductBarcode(1); !errors.Is(err, ErrNotFound) {
			t.Errorf("label after %s = %v", change, err)
		}
		testExec(t, s, `UPDATE products SET archived=0 WHERE id=1`)
		testExec(t, s, `UPDATE product_codes SET archived=0,symbology='Code128',raw_value=normalized_value WHERE product_id=1 AND scheme='demo_local'`)
	}
	testExec(t, s, `INSERT INTO product_codes(product_id,scheme,raw_value,normalized_value,symbology) VALUES(1,'demo_local','SHOPDEMO-AMBIGUOUS','SHOPDEMO-AMBIGUOUS','Code128')`)
	if _, err := s.ProductBarcode(1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ambiguous label = %v", err)
	}
}
func TestBarcodeHTTPReadOnlyAndOpaque(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, "")
	before := testProduct(t, s, 1)
	events := testCount(t, s, "catalog_events")
	sessions := testCount(t, s, "sessions")
	request := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/products/"+id+"/barcode.png", nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		a.showProductBarcode(w, r)
		return w
	}
	w := request("1")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("label response %d %v", w.Code, w.Header())
	}
	im, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	label, _ := s.ProductBarcode(1)
	if im.Bounds().Dx() != label.Width || im.Bounds().Dy() != label.Height {
		t.Fatal("dimensions disagree")
	}
	for y := 0; y < label.Height; y++ {
		for x := 0; x < barcodeQuietModules*barcodeModulePixels; x++ {
			for _, px := range []int{x, label.Width - x - 1} {
				r, g, b, a := im.At(px, y).RGBA()
				if r != 65535 || g != 65535 || b != 65535 || a != 65535 {
					t.Fatal("quiet zone is not opaque white")
				}
			}
		}
	}
	for x := barcodeQuietModules * barcodeModulePixels; x < label.Width-barcodeQuietModules*barcodeModulePixels; x += barcodeModulePixels {
		for dx := 1; dx < barcodeModulePixels; dx++ {
			if im.At(x, 80) != im.At(x+dx, 80) {
				t.Fatal("fractional module")
			}
		}
	}
	if after := testProduct(t, s, 1); after.Stock != before.Stock || testCount(t, s, "catalog_events") != events || testCount(t, s, "sessions") != sessions {
		t.Fatal("label read changed catalog/stock/session")
	}
	for _, id := range []string{"-1", "0", "not-an-id", "99999999"} {
		if got := request(id).Code; got != 404 {
			t.Errorf("%s status %d", id, got)
		}
	}
	testExec(t, s, `UPDATE products SET archived=1 WHERE id=1`)
	if request("1").Code != 404 {
		t.Fatal("archived label served")
	}
}

type barcodePixelFixture struct {
	Name   string `json:"name"`
	Code   string `json:"code"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Pixels string `json:"pixels"`
	Mode   string `json:"mode"`
}

func pixelFixture(t *testing.T, name, code, mode string, im image.Image) barcodePixelFixture {
	t.Helper()
	// Always encode and decode actual image bytes, not expected module patterns.
	var data bytes.Buffer
	if err := png.Encode(&data, im); err != nil {
		t.Fatal(err)
	}
	actual, err := png.Decode(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	gray := make([]byte, actual.Bounds().Dx()*actual.Bounds().Dy())
	for y := 0; y < actual.Bounds().Dy(); y++ {
		for x := 0; x < actual.Bounds().Dx(); x++ {
			gray[y*actual.Bounds().Dx()+x] = color.GrayModel.Convert(actual.At(x, y)).(color.Gray).Y
		}
	}
	return barcodePixelFixture{Name: name, Code: code, Mode: mode, Width: actual.Bounds().Dx(), Height: actual.Bounds().Dy(), Pixels: base64.StdEncoding.EncodeToString(gray)}
}
func TestBarcodePixelsIndependentZXing(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the independent pixel check; scripts/check.sh always runs with Node")
	}
	s := newTestStore(t)
	var fixtures []barcodePixelFixture
	products, err := s.Products("", "")
	if err != nil {
		t.Fatal(err)
	}
	// New catalog identities exercise the stored path, including changing the SKU.
	p := Product{Name: "Scanner fixture", Description: "Test", CategoryID: products[0].CategoryID, Icon: "bread", Price: 125, PriceBasis: 1, SaleUnit: "each", QuantityStep: 1}
	id, err := s.SaveProduct(p)
	if err != nil {
		t.Fatal(err)
	}
	products = append(products, Product{ID: id})
	for _, p := range products {
		label, err := s.ProductBarcode(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := productBarcodePNG(label.Code)
		if err != nil {
			t.Fatal(err)
		}
		im, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, pixelFixture(t, fmt.Sprintf("stored-%d", p.ID), label.Code, "exact", im))
	}
	for _, code := range []string{"SHOPDEMO-999999", "SHOPDEMO-1000000", "SHOPDEMO-9223372036854775807", "SHOPDEMO-STORED-NOT-SKU"} {
		data, err := productBarcodePNG(code)
		if err != nil {
			t.Fatal(err)
		}
		im, _ := png.Decode(bytes.NewReader(data))
		fixtures = append(fixtures, pixelFixture(t, code, code, "exact", im))
	}
	code := fixtures[0].Code
	data, _ := productBarcodePNG(code)
	base, _ := png.Decode(bytes.NewReader(data))
	w, h := base.Bounds().Dx(), base.Bounds().Dy()
	for _, angle := range []int{90, 180, 270} {
		dw, dh := w, h
		if angle != 180 {
			dw, dh = h, w
		}
		im := image.NewGray(image.Rect(0, 0, dw, dh))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dx, dy := w-x-1, h-y-1
				if angle == 90 {
					dx, dy = h-y-1, x
				}
				if angle == 270 {
					dx, dy = y, w-x-1
				}
				im.Set(dx, dy, base.At(x, y))
			}
		}
		fixtures = append(fixtures, pixelFixture(t, fmt.Sprintf("rotation-%d", angle), code, "exact", im))
	}
	small := image.NewGray(image.Rect(0, 0, w*2/3, h*2/3))
	for y := 0; y < small.Bounds().Dy(); y++ {
		for x := 0; x < small.Bounds().Dx(); x++ {
			small.Set(x, y, base.At(x*3/2, y*3/2))
		}
	}
	fixtures = append(fixtures, pixelFixture(t, "two-pixel-modules", code, "exact", small))
	var photo bytes.Buffer
	if err := jpeg.Encode(&photo, base, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	jpg, _ := jpeg.Decode(bytes.NewReader(photo.Bytes()))
	fixtures = append(fixtures, pixelFixture(t, "jpeg-quality-75", code, "exact", jpg))
	blur := image.NewGray(base.Bounds())
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			sum := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					sum += int(color.GrayModel.Convert(base.At(x+dx, y+dy)).(color.Gray).Y)
				}
			}
			blur.SetGray(x, y, color.Gray{Y: uint8(sum / 9)})
		}
	}
	fixtures = append(fixtures, pixelFixture(t, "moderate-blur", code, "exact", blur))
	skew := image.NewGray(image.Rect(0, 0, w+50, h+100))
	draw.Draw(skew, skew.Bounds(), image.White, image.Point{}, draw.Src)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			skew.Set(x+20, y+int(math.Round(float64(x-w/2)*.06))+50, base.At(x, y))
		}
	}
	fixtures = append(fixtures, pixelFixture(t, "moderate-skew", code, "exact", skew))
	blank := image.NewGray(base.Bounds())
	draw.Draw(blank, blank.Bounds(), image.White, image.Point{}, draw.Src)
	fixtures = append(fixtures, pixelFixture(t, "blank", code, "none", blank))
	damaged := image.NewGray(base.Bounds())
	draw.Draw(damaged, damaged.Bounds(), base, image.Point{}, draw.Src)
	right := w - barcodeQuietModules*barcodeModulePixels - 13*barcodeModulePixels
	draw.Draw(damaged, image.Rect(right-11*barcodeModulePixels, 0, right, h), image.White, image.Point{}, draw.Src)
	fixtures = append(fixtures, pixelFixture(t, "damaged-checksum", code, "none", damaged))
	cropped := image.NewGray(image.Rect(0, 0, w-100, h))
	draw.Draw(cropped, cropped.Bounds(), base, image.Point{X: 100}, draw.Src)
	fixtures = append(fixtures, pixelFixture(t, "cropped-start-and-quiet-zone", code, "none", cropped))
	contrast := image.NewGray(base.Bounds())
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.GrayModel.Convert(base.At(x, y)).(color.Gray)
			contrast.SetGray(x, y, color.Gray{Y: 118 + c.Y/25})
		}
	}
	fixtures = append(fixtures, pixelFixture(t, "poor-contrast", code, "safe", contrast))
	otherCode := "SHOPDEMO-999998"
	otherData, _ := productBarcodePNG(otherCode)
	other, _ := png.Decode(bytes.NewReader(otherData))
	multi := image.NewGray(image.Rect(0, 0, max(w, other.Bounds().Dx()), 2*h+30))
	draw.Draw(multi, multi.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(multi, base.Bounds(), base, image.Point{}, draw.Src)
	draw.Draw(multi, image.Rect(0, h+30, other.Bounds().Dx(), 2*h+30), other, image.Point{}, draw.Src)
	fixtures = append(fixtures, pixelFixture(t, "two-labels", code, "multiple", multi))
	duplicate := image.NewGray(image.Rect(0, 0, w*2+30, h))
	draw.Draw(duplicate, duplicate.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(duplicate, base.Bounds(), base, image.Point{}, draw.Src)
	draw.Draw(duplicate, image.Rect(w+30, 0, w*2+30, h), base, image.Point{}, draw.Src)
	fixtures = append(fixtures, pixelFixture(t, "duplicate-side-by-side-labels", code, "multiple", duplicate))
	duplicateStacked := image.NewGray(image.Rect(0, 0, w, h*2+30))
	draw.Draw(duplicateStacked, duplicateStacked.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(duplicateStacked, base.Bounds(), base, image.Point{}, draw.Src)
	draw.Draw(duplicateStacked, image.Rect(0, h+30, w, h*2+30), base, image.Point{}, draw.Src)
	fixtures = append(fixtures, pixelFixture(t, "duplicate-stacked-labels", code, "multiple", duplicateStacked))
	qrValue := "https://example.test/handheld/#pair=ABCDEFGHIJKLMNOP2345678901"
	qrData, err := handheldPairingQRDataURL(qrValue)
	if err != nil {
		t.Fatal(err)
	}
	qrBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(qrData), "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	qrImage, err := png.Decode(bytes.NewReader(qrBytes))
	if err != nil {
		t.Fatal(err)
	}
	fixtures = append(fixtures, pixelFixture(t, "pairing-qr", qrValue, "qr", qrImage))
	raw, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "../../scripts/barcode_pixels_check.cjs")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("independent ZXing: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
func TestBarcodePairingQRIsInline(t *testing.T) {
	data, err := handheldPairingQRDataURL("https://example.test/handheld/#pair=ABCDEFGHIJKLMNOP2345678901")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(data), "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 24; y++ {
		for x := 0; x < im.Bounds().Dx(); x++ {
			r, g, b, a := im.At(x, y).RGBA()
			if r != 65535 || g != 65535 || b != 65535 || a != 65535 {
				t.Fatal("QR quiet zone")
			}
		}
	}
}
