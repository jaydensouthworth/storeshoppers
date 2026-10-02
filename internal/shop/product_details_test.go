package shop

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testDetails(t *testing.T, s *Store, id int64) ProductDetails {
	t.Helper()
	d, err := s.ProductDetails(id)
	if err != nil {
		t.Fatalf("ProductDetails(%d): %v", id, err)
	}
	return d
}

func detailsFixture() ProductDetails {
	return ProductDetails{
		Kind: "food", Body: "A longer product story.\nKeep chilled after opening.",
		PackageLabel: "One 250 g tub", PackageDetails: "Recyclable tub with a resealable lid.",
		Nutrition: &DemoNutrition{ServingLabel: "Per 100 g", EnergyKcal: 150, FatTenths: 35, CarbohydrateTenths: 200, ProteinTenths: 80, SodiumMG: 125},
	}
}

func newDetailsProduct(t *testing.T, s *Store) Product {
	t.Helper()
	return Product{Name: "Product details fixture", Description: "Short catalog description", CategoryID: testProduct(t, s, 1).CategoryID, Icon: "apple", Price: 499, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}
}

func TestProductDetailsMissingAndUnknownProducts(t *testing.T) {
	s := newTestStore(t)
	p := testCreateProduct(t, s, nil)
	before := fingerprintTest(t, s.db)
	want := ProductDetails{ProductID: p.ID, Kind: "unspecified"}
	if got := testDetails(t, s, p.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("unconfigured product details = %+v, want %+v", got, want)
	}
	if _, err := s.ProductDetails(999999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown product details = %v, want ErrNotFound", err)
	}
	unknown := p
	unknown.ID = 999999999
	if id, err := s.SaveProductWithDetails(unknown, detailsFixture()); id != 0 || !errors.Is(err, ErrNotFound) {
		t.Errorf("save details for unknown product = %d, %v, want 0, ErrNotFound", id, err)
	}
	if after := fingerprintTest(t, s.db); after != before {
		t.Error("details lookup invented or changed saved data")
	}
}

func TestProductDetailsCreateAndLegacySavePreserveMetadata(t *testing.T) {
	s := newTestStore(t)
	p, d := newDetailsProduct(t, s), detailsFixture()
	events := testCount(t, s, "catalog_events")
	id, err := s.SaveProductWithDetails(p, d)
	if err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, id)
	d.ProductID = id
	if got := testDetails(t, s, id); !reflect.DeepEqual(got, d) {
		t.Errorf("created details = %+v, want %+v", got, d)
	}
	if p.Stock != 0 || p.Version != 1 || p.CatalogVersion != 1 || p.PriceVersion != 1 {
		t.Errorf("details creation changed initial stock or versions: %+v", p)
	}
	if got := testCount(t, s, "catalog_events"); got != events+1 {
		t.Errorf("combined create audit count = %d, want %d", got, events+1)
	}
	if err := s.Adjust(id, 7, p.Version, "Stock supplied separately from details"); err != nil {
		t.Fatal(err)
	}
	original := testCatalogProduct(t, s, id)
	// The open metadata form still has the pre-restock inventory version and
	// quantity. Its catalog version is current, so it must retain the restock.
	d.Body = "A revised full product description."
	d.PackageDetails = "Two independently sealed portions."
	d.Nutrition.EnergyKcal++
	if _, err := s.SaveProductWithDetails(p, d); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, id)
	want := original
	want.CatalogVersion++
	if p != want {
		t.Errorf("details-only save changed product or wrong version domain: got %+v, want %+v", p, want)
	}
	if got := testDetails(t, s, id); !reflect.DeepEqual(got, d) {
		t.Errorf("details update not saved: %+v", got)
	}
	p.Name = "Legacy caller edits the product"
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	if got := testDetails(t, s, id); !reflect.DeepEqual(got, d) {
		t.Error("SaveProduct removed or rewrote existing product details")
	}
	p = testCatalogProduct(t, s, id)
	if p.Stock != original.Stock || p.Version != original.Version || p.PriceVersion != original.PriceVersion || p.CatalogVersion != original.CatalogVersion+2 {
		t.Errorf("legacy metadata save crossed version domains: %+v", p)
	}
	if got := testCount(t, s, "catalog_events"); got != events+3 {
		t.Errorf("combined create and two edits audit count = %d, want %d", got, events+3)
	}
}

func TestProductDetailsKindRemainsSeparateFromTaxonomy(t *testing.T) {
	s := newTestStore(t)
	typ, err := s.SaveTaxonomy("type", 0, 0, "Household favorites")
	if err != nil {
		t.Fatal(err)
	}
	p := testCreateProduct(t, s, func(p *Product) { p.TypeID = typ })
	category, productType := p.CategoryID, p.TypeID
	for _, kind := range []string{"food", "nonfood", "unspecified"} {
		d := ProductDetails{Kind: kind, Body: "Manager-authored details", PackageLabel: "One item"}
		if _, err := s.SaveProductWithDetails(p, d); err != nil {
			t.Fatalf("save %s: %v", kind, err)
		}
		p = testCatalogProduct(t, s, p.ID)
		if p.CategoryID != category || p.TypeID != productType || p.ProductType != "Household favorites" {
			t.Errorf("%s kind rewrote independent taxonomy: %+v", kind, p)
		}
		if got := testDetails(t, s, p.ID); got.Kind != kind || got.Nutrition != nil {
			t.Errorf("%s kind inferred nutrition or another kind: %+v", kind, got)
		}
	}
	if _, err := s.SaveTaxonomy("type", typ, 1, "Food-shaped decorative items"); err != nil {
		t.Fatal(err)
	}
	if got := testDetails(t, s, p.ID); got.Kind != "unspecified" {
		t.Errorf("taxonomy rename inferred a product kind: %+v", got)
	}
}

func TestProductDetailsValidationIsAtomic(t *testing.T) {
	s := newTestStore(t)
	p := testCreateProduct(t, s, nil)
	if _, err := s.SaveProductWithDetails(p, detailsFixture()); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, p.ID)
	cases := []struct {
		name   string
		change func(*ProductDetails)
	}{
		{"unknown kind", func(d *ProductDetails) { d.Kind = "beverage" }},
		{"negative product ID", func(d *ProductDetails) { d.ProductID = -1 }},
		{"another product ID", func(d *ProductDetails) { d.ProductID = p.ID + 100 }},
		{"long body", func(d *ProductDetails) { d.Body = strings.Repeat("é", 1601) }},
		{"long package label", func(d *ProductDetails) { d.PackageLabel = strings.Repeat("é", 81) }},
		{"long package details", func(d *ProductDetails) { d.PackageDetails = strings.Repeat("é", 241) }},
		{"long serving", func(d *ProductDetails) { d.Nutrition.ServingLabel = strings.Repeat("é", 81) }},
		{"invalid body UTF-8", func(d *ProductDetails) { d.Body = "body\xff" }},
		{"invalid package label UTF-8", func(d *ProductDetails) { d.PackageLabel = "label\xff" }},
		{"invalid package details UTF-8", func(d *ProductDetails) { d.PackageDetails = "details\xff" }},
		{"invalid serving UTF-8", func(d *ProductDetails) { d.Nutrition.ServingLabel = "serving\xff" }},
		{"body with NUL", func(d *ProductDetails) { d.Body = "body\x00truncated" }},
		{"package label with NUL", func(d *ProductDetails) { d.PackageLabel = "label\x00truncated" }},
		{"package details with NUL", func(d *ProductDetails) { d.PackageDetails = "details\x00truncated" }},
		{"serving with NUL", func(d *ProductDetails) { d.Nutrition.ServingLabel = "serving\x00truncated" }},
		{"nutrition for nonfood", func(d *ProductDetails) { d.Kind = "nonfood" }},
		{"nutrition for unspecified", func(d *ProductDetails) { d.Kind = "unspecified" }},
		{"missing serving", func(d *ProductDetails) { d.Nutrition.ServingLabel = "" }},
		{"blank serving", func(d *ProductDetails) { d.Nutrition.ServingLabel = " \n\t " }},
		{"negative energy", func(d *ProductDetails) { d.Nutrition.EnergyKcal = -1 }},
		{"large energy", func(d *ProductDetails) { d.Nutrition.EnergyKcal = 10001 }},
		{"negative fat", func(d *ProductDetails) { d.Nutrition.FatTenths = -1 }},
		{"large fat", func(d *ProductDetails) { d.Nutrition.FatTenths = 100001 }},
		{"negative carbohydrate", func(d *ProductDetails) { d.Nutrition.CarbohydrateTenths = -1 }},
		{"large carbohydrate", func(d *ProductDetails) { d.Nutrition.CarbohydrateTenths = 100001 }},
		{"negative protein", func(d *ProductDetails) { d.Nutrition.ProteinTenths = -1 }},
		{"large protein", func(d *ProductDetails) { d.Nutrition.ProteinTenths = 100001 }},
		{"negative sodium", func(d *ProductDetails) { d.Nutrition.SodiumMG = -1 }},
		{"large sodium", func(d *ProductDetails) { d.Nutrition.SodiumMG = 1000001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := detailsFixture()
			tc.change(&d)
			edit := p
			edit.Name, edit.Price = "Must not commit with invalid details", p.Price+100
			before := fingerprintTest(t, s.db)
			if id, err := s.SaveProductWithDetails(edit, d); id != 0 || !errors.Is(err, ErrInvalid) {
				t.Errorf("invalid details save = %d, %v, want 0, ErrInvalid", id, err)
			}
			if after := fingerprintTest(t, s.db); after != before {
				t.Error("invalid details partially changed product, metadata, identity, or audit")
			}
		})
	}
	for _, initialStock := range []int64{-1, 1} {
		p := newDetailsProduct(t, s)
		p.Stock = initialStock
		before := fingerprintTest(t, s.db)
		if id, err := s.SaveProductWithDetails(p, detailsFixture()); id != 0 || !errors.Is(err, ErrInvalid) {
			t.Errorf("initial stock %d = %d, %v, want 0, ErrInvalid", initialStock, id, err)
		}
		if after := fingerprintTest(t, s.db); after != before {
			t.Error("rejected initial stock left a product or details")
		}
	}
}

func TestProductDetailsBoundsAndOptionalNutrition(t *testing.T) {
	s := newTestStore(t)
	p := testCreateProduct(t, s, nil)
	d := detailsFixture()
	d.Body, d.PackageLabel, d.PackageDetails = strings.Repeat("é", 1600), strings.Repeat("包", 80), strings.Repeat("é", 240)
	d.Nutrition = &DemoNutrition{ServingLabel: strings.Repeat("份", 80), EnergyKcal: 10000, FatTenths: 100000, CarbohydrateTenths: 100000, ProteinTenths: 100000, SodiumMG: 1000000}
	if _, err := s.SaveProductWithDetails(p, d); err != nil {
		t.Fatalf("valid character and nutrition upper bounds: %v", err)
	}
	d.ProductID = p.ID
	if got := testDetails(t, s, p.ID); !reflect.DeepEqual(got, d) {
		t.Error("valid Unicode details or nutrition integer values were truncated")
	}
	p = testCatalogProduct(t, s, p.ID)
	d = ProductDetails{ProductID: p.ID, Kind: "food", Body: `Literal <strong>text</strong> & "quoted" content`, Nutrition: &DemoNutrition{ServingLabel: "One serving"}}
	if _, err := s.SaveProductWithDetails(p, d); err != nil {
		t.Fatalf("explicit zero nutrition: %v", err)
	}
	if got := testDetails(t, s, p.ID); !reflect.DeepEqual(got, d) || got.Nutrition == nil {
		t.Errorf("explicit zero nutrition or literal plain text changed: %+v", got)
	}
	p = testCatalogProduct(t, s, p.ID)
	d.Nutrition = nil
	if _, err := s.SaveProductWithDetails(p, d); err != nil {
		t.Fatalf("clear optional nutrition: %v", err)
	}
	if got := testDetails(t, s, p.ID); !reflect.DeepEqual(got, d) || got.Nutrition != nil {
		t.Errorf("absent nutrition became an all-zero nutrition claim: %+v", got)
	}
}

func TestProductDetailsShareCatalogConflictAndRollback(t *testing.T) {
	for _, winningSave := range []string{"catalog", "details"} {
		t.Run(winningSave, func(t *testing.T) {
			s := newTestStore(t)
			p := testCreateProduct(t, s, nil)
			if _, err := s.SaveProductWithDetails(p, detailsFixture()); err != nil {
				t.Fatal(err)
			}
			p = testCatalogProduct(t, s, p.ID)
			winner := p
			winner.Name = "The winning edit"
			if winningSave == "catalog" {
				if _, err := s.SaveProduct(winner); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.SaveProductWithDetails(winner, ProductDetails{Kind: "nonfood", Body: "The winning details"}); err != nil {
				t.Fatal(err)
			}
			before := fingerprintTest(t, s.db)
			p.Name, p.Price = "Stale product edit", p.Price+100
			if id, err := s.SaveProductWithDetails(p, ProductDetails{Kind: "food", Body: "Stale details"}); id != 0 || !errors.Is(err, ErrConflict) {
				t.Errorf("stale combined save = %d, %v, want 0, ErrConflict", id, err)
			}
			if id, err := s.SaveProduct(p); id != 0 || !errors.Is(err, ErrConflict) {
				t.Errorf("stale legacy save = %d, %v, want 0, ErrConflict", id, err)
			}
			if after := fingerprintTest(t, s.db); after != before {
				t.Error("stale save altered winning product, details, audit, or sequence")
			}
		})
	}
}

func TestProductDetailsAuditAndStorageFailureRollback(t *testing.T) {
	for _, failure := range []string{"audit", "details"} {
		for _, action := range []string{"create", "edit"} {
			t.Run(failure+"/"+action, func(t *testing.T) {
				s := newTestStore(t)
				p := newDetailsProduct(t, s)
				if action == "edit" {
					id, err := s.SaveProductWithDetails(p, detailsFixture())
					if err != nil {
						t.Fatal(err)
					}
					p = testCatalogProduct(t, s, id)
					p.Name, p.Price = "Must roll back product edit", p.Price+1
				}
				if failure == "audit" {
					testExec(t, s, `CREATE TRIGGER reject_details_audit BEFORE INSERT ON catalog_events BEGIN SELECT RAISE(ABORT,'injected details audit failure'); END`)
				} else {
					testExec(t, s, `CREATE TRIGGER reject_details_insert BEFORE INSERT ON product_details BEGIN SELECT RAISE(ABORT,'injected details storage failure'); END`)
					testExec(t, s, `CREATE TRIGGER reject_details_update BEFORE UPDATE ON product_details BEGIN SELECT RAISE(ABORT,'injected details storage failure'); END`)
				}
				before := fingerprintTest(t, s.db)
				if id, err := s.SaveProductWithDetails(p, ProductDetails{Kind: "nonfood", Body: "Must roll back details"}); id != 0 || err == nil || !strings.Contains(err.Error(), "injected details") {
					t.Errorf("injected %s failure = %d, %v", failure, id, err)
				}
				if after := fingerprintTest(t, s.db); after != before {
					t.Error("failed combined save left product, details, audit, code, or sequence mutations")
				}
			})
		}
	}
}

func TestProductDetailsArchiveRestoreRetainsMetadata(t *testing.T) {
	s := newTestStore(t)
	p := testCreateProduct(t, s, nil)
	d := detailsFixture()
	if _, err := s.SaveProductWithDetails(p, d); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, p.ID)
	d.ProductID = p.ID
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	if got := testDetails(t, s, p.ID); !reflect.DeepEqual(got, d) {
		t.Error("archiving product lost details or nutrition")
	}
	archived := testCatalogProduct(t, s, p.ID)
	before := fingerprintTest(t, s.db)
	if id, err := s.SaveProductWithDetails(archived, ProductDetails{Kind: "nonfood", Body: "Edit while archived"}); id != 0 || !errors.Is(err, ErrConflict) {
		t.Errorf("archived save = %d, %v, want 0, ErrConflict", id, err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Error("rejected archived edit changed retained data")
	}
	if err := s.RestoreProduct(p.ID, archived.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	if got := testDetails(t, s, p.ID); !reflect.DeepEqual(got, d) {
		t.Error("restoring product replaced retained details or nutrition")
	}
}

func TestProductDetailsMetadataEditPreservesQuoteHoldsAndReceipts(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	orderID := testCheckout(t, s, owner.ID)
	receipt := testOrder(t, s, orderID, owner.ID)
	testCart(t, s, owner.ID, 1, 2)
	session, basket := testSession(t, s, owner.ID), testBasket(t, s, owner.ID)
	p := testCatalogProduct(t, s, 1)
	before := p
	if _, err := s.SaveProductWithDetails(p, detailsFixture()); err != nil {
		t.Fatal(err)
	}
	if p = testCatalogProduct(t, s, p.ID); p.Stock != before.Stock || p.Reserved != before.Reserved || p.Version != before.Version || p.PriceVersion != before.PriceVersion || p.CatalogVersion != before.CatalogVersion+1 {
		t.Errorf("details edit changed inventory, reservations, or price versions: %+v", p)
	}
	if got := testBasket(t, s, owner.ID); got.Quote != basket.Quote || got.Total != basket.Total || got.Revision != basket.Revision || got.HoldUntil != basket.HoldUntil || !got.CanCheckout {
		t.Errorf("details-only edit invalidated quote or held basket: %+v", got)
	}
	if got := testOrder(t, s, orderID, owner.ID); !reflect.DeepEqual(got, receipt) {
		t.Error("details edit rewrote a committed receipt or working order")
	}
	if got := testSession(t, s, owner.ID); got != session {
		t.Error("details edit changed checkout/session ownership state")
	}
	id, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, basket.Quote)
	if err != nil {
		t.Fatalf("unchanged quote rejected after details-only edit: %v", err)
	}
	if got := testOrder(t, s, id, owner.ID); got.Total != basket.Total {
		t.Errorf("checkout total = %d, want quoted %d", got.Total, basket.Total)
	}
}
