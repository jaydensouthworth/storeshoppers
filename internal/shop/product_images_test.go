package shop

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"instore-shopper/internal/productimage"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func imageFixture(t *testing.T, shade uint8) (productimage.Result, []byte) {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			im.SetNRGBA(x, y, color.NRGBA{R: shade, G: uint8(x * 7), B: uint8(y * 9), A: 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, im); err != nil {
		t.Fatal(err)
	}
	n, err := productimage.NormalizeBytes(raw.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return n, raw.Bytes()
}

func TestProductImagesAttachReplayAndRestorePreserveCommerce(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	id := testCheckout(t, s, owner.ID)
	before := testOrder(t, s, id, owner.ID)
	p := testProduct(t, s, 1)
	im, _ := imageFixture(t, 80)
	key := token()
	if err := s.AttachProductImage(1, p.CatalogVersion, key, im); err != nil {
		t.Fatal(err)
	}
	first := fingerprintTest(t, s.db)
	if err := s.AttachProductImage(1, p.CatalogVersion, key, im); err != nil {
		t.Fatal(err)
	}
	if fingerprintTest(t, s.db) != first {
		t.Fatal("replay changed image, association or audit")
	}
	current := testProduct(t, s, 1)
	if current.ImageHash != im.SHA256 || current.CatalogVersion != p.CatalogVersion+1 || current.Version != p.Version || current.PriceVersion != p.PriceVersion || current.Stock != p.Stock {
		t.Fatalf("product state changed incorrectly: %+v", current)
	}
	after := testOrder(t, s, id, owner.ID)
	if after.Total != before.Total || after.WorkingTotal != before.WorkingTotal || after.Items[0] != before.Items[0] {
		t.Fatal("image edit rewrote receipt")
	}
	if err := s.UseProductIllustration(1, current.CatalogVersion, token()); err != nil {
		t.Fatal(err)
	}
	if testProduct(t, s, 1).ImageHash != "" {
		t.Fatal("illustration did not detach image")
	}
	retained, err := s.ProductImage(im.SHA256)
	if err != nil || !bytes.Equal(retained.Master.Data, im.Master.Data) {
		t.Fatal("detached asset was lost", err)
	}
}

func TestProductImagesRejectStaleInvalidAndRollbackAudit(t *testing.T) {
	s := newTestStore(t)
	p := testProduct(t, s, 1)
	im, _ := imageFixture(t, 30)
	if err := s.AttachProductImage(1, p.CatalogVersion+1, token(), im); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	bad := im
	bad.SHA256 = token()
	if err := s.AttachProductImage(1, p.CatalogVersion, token(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	testExec(t, s, `CREATE TRIGGER reject_image_audit BEFORE INSERT ON catalog_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	before := fingerprintTest(t, s.db)
	if err := s.AttachProductImage(1, p.CatalogVersion, token(), im); err == nil {
		t.Fatal("audit failure accepted")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("failed image save left blob/association/command behind")
	}
}

func TestProductImagesConcurrentAssociationAndDedup(t *testing.T) {
	s := newTestStore(t)
	p := testProduct(t, s, 1)
	im, _ := imageFixture(t, 60)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.AttachProductImage(1, p.CatalogVersion, token(), im) }()
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("outcomes %d/%d", ok, conflict)
	}
	p2 := testProduct(t, s, 2)
	if err := s.AttachProductImage(2, p2.CatalogVersion, token(), im); err != nil {
		t.Fatal(err)
	}
	stats, err := s.ImageLibraryStats()
	if err != nil || stats.Count != 1 || stats.Bytes != int64(len(im.Master.Data)+len(im.Thumbnail.Data)) {
		t.Fatalf("dedup %+v %v", stats, err)
	}
}

func TestProductImagesPersistAndResetRetainsLibraryAndBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	im, _ := imageFixture(t, 120)
	p := testProduct(t, s, 1)
	if err = s.AttachProductImage(1, p.CatalogVersion, token(), im); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err = s.ClaimImagePreview(); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if testProduct(t, s, 1).ImageHash != im.SHA256 {
		t.Fatal("restart lost image")
	}
	if _, err = s.ResetDemo(DemoResetOptions{}); err != nil {
		t.Fatal(err)
	}
	if testProduct(t, s, 1).ImageHash != "" {
		t.Fatal("reset retained edited association")
	}
	if _, err = s.ProductImage(im.SHA256); err != nil {
		t.Fatal("reset purged recovery artwork", err)
	}
	if err = s.ClaimImagePreview(); !errors.Is(err, ErrImageRate) {
		t.Fatal("reset replenished preview budget", err)
	}
}

func TestProductImageBudgetExpiryAndQuota(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(60000, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < 50; i++ {
		if i > 0 && i%10 == 0 {
			now = now.Add(time.Minute)
		}
		if err := s.ClaimImagePreview(); err != nil {
			t.Fatal(i, err)
		}
	}
	now = now.Add(time.Minute)
	if err := s.ClaimImagePreview(); !errors.Is(err, ErrImageRate) {
		t.Fatal("daily budget not enforced", err)
	}
	now = now.Add(24 * time.Hour)
	if err := s.ClaimImagePreview(); err != nil {
		t.Fatal(err)
	}
	// All records below are actual normalized variants with distinct identities.
	for i := 0; i < maxProductImages; i++ {
		im, _ := imageFixture(t, uint8(i))
		p := testProduct(t, s, 1)
		if err := s.AttachProductImage(1, p.CatalogVersion, token(), im); err != nil {
			t.Fatal(i, err)
		}
	}
	im, _ := imageFixture(t, 255)
	p := testProduct(t, s, 1)
	before := fingerprintTest(t, s.db)
	if err := s.AttachProductImage(1, p.CatalogVersion, token(), im); !errors.Is(err, ErrImageQuota) {
		t.Fatal(err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("quota failure changed database")
	}
}
