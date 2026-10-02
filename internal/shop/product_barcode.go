package shop

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/boombuler/barcode/qr"
)

const barcodeModulePixels = 3
const barcodeQuietModules = 12
const barcodeHeight = 180

// ProductBarcodeLabel is catalog identity, not a quantity, weight or permission.
// Only the stored active demo-local Code 128 record is a label source.
type ProductBarcodeLabel struct {
	ProductID     int64
	Code          string
	Width, Height int
}

func (l ProductBarcodeLabel) ImageURL() string {
	return fmt.Sprintf("/products/%d/barcode.png", l.ProductID)
}

func (s *Store) ProductBarcode(id int64) (*ProductBarcodeLabel, error) {
	rows, err := s.db.Query(`SELECT pc.normalized_value FROM product_codes pc JOIN products p ON p.id=pc.product_id WHERE p.id=? AND p.archived=0 AND pc.archived=0 AND pc.scheme='demo_local' AND pc.symbology='Code128' AND pc.raw_value=pc.normalized_value ORDER BY pc.id LIMIT 2`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	label := &ProductBarcodeLabel{ProductID: id, Height: barcodeHeight}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	if err := rows.Scan(&label.Code); err != nil {
		return nil, err
	}
	// An ambiguous identity needs catalog repair, never an arbitrary selection.
	if rows.Next() {
		return nil, ErrNotFound
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	b, err := encodeDemoBarcode(label.Code)
	if err != nil {
		return nil, ErrNotFound
	}
	label.Width = (b.Bounds().Dx() + 2*barcodeQuietModules) * barcodeModulePixels
	return label, nil
}

func encodeDemoBarcode(code string) (barcode.Barcode, error) {
	if !strings.HasPrefix(code, "SHOPDEMO-") || len(code) <= len("SHOPDEMO-") || len(code) > 64 {
		return nil, ErrInvalid
	}
	for _, c := range code {
		if c < 33 || c > 126 {
			return nil, ErrInvalid
		}
	}
	return code128.Encode(code)
}

func productBarcodePNG(code string) ([]byte, error) {
	b, err := encodeDemoBarcode(code)
	if err != nil {
		return nil, err
	}
	// Integer module scaling and explicit opaque quiet zones survive both themes.
	im := image.NewGray(image.Rect(0, 0, (b.Bounds().Dx()+2*barcodeQuietModules)*barcodeModulePixels, barcodeHeight))
	draw.Draw(im, im.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	for x := 0; x < b.Bounds().Dx(); x++ {
		r, _, _, _ := b.At(x, 0).RGBA()
		if r == 0 {
			draw.Draw(im, image.Rect((x+barcodeQuietModules)*barcodeModulePixels, 12, (x+barcodeQuietModules+1)*barcodeModulePixels, barcodeHeight-12), image.Black, image.Point{}, draw.Src)
		}
	}
	var out bytes.Buffer
	err = png.Encode(&out, im)
	return out.Bytes(), err
}

// Pairing QR bytes stay inline in the manager response and are never sent to a
// third-party QR service. The caller supplies its trusted origin/fragment URL.
func handheldPairingQRDataURL(value string) (template.URL, error) {
	if len(value) == 0 || len(value) > 2048 {
		return "", ErrInvalid
	}
	b, err := qr.Encode(value, qr.M, qr.Auto)
	if err != nil {
		return "", err
	}
	const scale = 6
	const quiet = 4
	w := b.Bounds().Dx()
	im := image.NewGray(image.Rect(0, 0, (w+2*quiet)*scale, (w+2*quiet)*scale))
	draw.Draw(im, im.Bounds(), image.White, image.Point{}, draw.Src)
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := b.At(x, y).RGBA()
			if r == 0 {
				draw.Draw(im, image.Rect((x+quiet)*scale, (y+quiet)*scale, (x+quiet+1)*scale, (y+quiet+1)*scale), image.Black, image.Point{}, draw.Src)
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		return "", err
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes())), nil
}
