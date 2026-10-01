package shop

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testCatalogProduct(t *testing.T, s *Store, id int64) Product {
	t.Helper()
	products, err := s.CatalogProducts()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range products {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("catalog product %d absent", id)
	return Product{}
}

func testTaxonomy(t *testing.T, s *Store, kind string, id int64) Taxonomy {
	t.Helper()
	all, err := s.Taxonomies(kind, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tax := range all {
		if tax.ID == id {
			return tax
		}
	}
	t.Fatalf("%s taxonomy %d absent", kind, id)
	return Taxonomy{}
}

func testCreateProduct(t *testing.T, s *Store, change func(*Product)) Product {
	t.Helper()
	p := Product{Name: "Test product", Description: "A newly managed item", CategoryID: testProduct(t, s, 1).CategoryID, Icon: "apple", Price: 499, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}
	if change != nil {
		change(&p)
	}
	id, err := s.SaveProduct(p)
	if err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	return testCatalogProduct(t, s, id)
}

func TestTaxonomyNormalizationVersionsAndStableArchives(t *testing.T) {
	for _, kind := range []string{"category", "type"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			id, err := s.SaveTaxonomy(kind, 0, 0, "  Special\t  Selections  ")
			if err != nil {
				t.Fatal(err)
			}
			original := testTaxonomy(t, s, kind, id)
			if original.Name != "Special Selections" || original.Version != 1 || original.Archived {
				t.Fatalf("created taxonomy: %+v", original)
			}
			if _, err = s.SaveTaxonomy(kind, 0, 0, "special selections"); !errors.Is(err, ErrDuplicate) {
				t.Errorf("case-insensitive duplicate = %v", err)
			}
			if _, err = s.SaveTaxonomy(kind, id, original.Version, "Market Favorites"); err != nil {
				t.Fatal(err)
			}
			renamed := testTaxonomy(t, s, kind, id)
			if renamed.ID != id || renamed.Version != 2 || renamed.Name != "Market Favorites" {
				t.Errorf("renamed taxonomy: %+v", renamed)
			}
			if _, err = s.SaveTaxonomy(kind, id, original.Version, "Stale rename"); !errors.Is(err, ErrConflict) {
				t.Errorf("stale rename = %v", err)
			}
			if err = s.ArchiveTaxonomy(kind, id, original.Version); !errors.Is(err, ErrConflict) {
				t.Errorf("stale archive = %v", err)
			}
			if err = s.ArchiveTaxonomy(kind, id, renamed.Version); err != nil {
				t.Fatal(err)
			}
			archived := testTaxonomy(t, s, kind, id)
			if !archived.Archived || archived.Version != renamed.Version+1 {
				t.Errorf("archive: %+v", archived)
			}
			active, err := s.Taxonomies(kind, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, tax := range active {
				if tax.ID == id {
					t.Error("archived taxonomy in active list")
				}
			}
			if _, err = s.SaveTaxonomy(kind, 0, 0, " MARKET FAVORITES "); !errors.Is(err, ErrDuplicate) {
				t.Errorf("archived name reused: %v", err)
			}
			next, err := s.SaveTaxonomy(kind, 0, 0, "Another department")
			if err != nil || next <= id {
				t.Errorf("new taxonomy identity = %d, %v; prior %d", next, err, id)
			}
		})
	}
}

func TestTaxonomyValidationAndReferenceGuards(t *testing.T) {
	s := newTestStore(t)
	for _, kind := range []string{"category", "type"} {
		for _, name := range []string{"", " \n\t ", strings.Repeat("x", 81)} {
			if _, err := s.SaveTaxonomy(kind, 0, 0, name); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s name %q: %v", kind, name, err)
			}
		}
	}
	if _, err := s.Taxonomies("products", true); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid kind = %v", err)
	}
	if _, err := s.SaveTaxonomy("products", 0, 0, "Wrong kind"); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid create kind = %v", err)
	}
	category, err := s.SaveTaxonomy("category", 0, 0, "Seasonal")
	if err != nil {
		t.Fatal(err)
	}
	typ, err := s.SaveTaxonomy("type", 0, 0, "Test fruit specialty")
	if err != nil {
		t.Fatal(err)
	}
	p := testCreateProduct(t, s, func(p *Product) { p.CategoryID = category; p.TypeID = typ })
	for _, tc := range []struct {
		kind string
		id   int64
	}{{"category", category}, {"type", typ}} {
		if err := s.ArchiveTaxonomy(tc.kind, tc.id, 1); !errors.Is(err, ErrReferenced) {
			t.Errorf("archive %s referenced by active product = %v", tc.kind, err)
		}
		if got := testTaxonomy(t, s, tc.kind, tc.id); got.Archived || got.Version != 1 {
			t.Errorf("rejected archive changed taxonomy: %+v", got)
		}
	}
	p.CategoryID = testProduct(t, s, 1).CategoryID
	p.TypeID = 0
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveTaxonomy("category", category, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveTaxonomy("type", typ, 1); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Product){func(p *Product) { p.CategoryID = category }, func(p *Product) { p.TypeID = typ }} {
		p := testCatalogProduct(t, s, p.ID)
		change(&p)
		if _, err := s.SaveProduct(p); err == nil {
			t.Error("product allowed archived taxonomy")
		}
	}
	// Renames use stable foreign keys and remain visible through storefront filters.
	original := testProduct(t, s, 1)
	cat := testTaxonomy(t, s, "category", original.CategoryID)
	if _, err := s.SaveTaxonomy("category", cat.ID, cat.Version, "Fresh produce"); err != nil {
		t.Fatal(err)
	}
	updated := testProduct(t, s, 1)
	if updated.CategoryID != original.CategoryID || updated.Category != "Fresh produce" {
		t.Errorf("rename lost assignment: %+v", updated)
	}
	filtered, err := s.Products("", "Fresh produce")
	if err != nil || len(filtered) < 2 {
		t.Errorf("renamed filter: %+v, %v", filtered, err)
	}
}

func TestProductCreationIdentifiersAndVersionIsolation(t *testing.T) {
	s := newTestStore(t)
	p := testCreateProduct(t, s, func(p *Product) { p.SKU = " fresh-apples_2026 " })
	if p.SKU != "FRESH-APPLES_2026" || p.Stock != 0 || p.Version != 1 || p.CatalogVersion != 1 || p.PriceVersion != 1 {
		t.Fatalf("new product defaults/identity: %+v", p)
	}
	if p.ID <= 8 || p.CategoryID < 1 || p.SaleUnit != "each" || p.PriceBasis != 1 || p.QuantityStep != 1 {
		t.Errorf("new product metadata: %+v", p)
	}
	duplicate := p
	duplicate.ID = 0
	duplicate.SKU = "fresh-apples_2026"
	if _, err := s.SaveProduct(duplicate); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate canonical SKU = %v", err)
	}
	saved := p
	p.Name = "Better name"
	p.Description = "Updated description"
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	edited := testCatalogProduct(t, s, p.ID)
	if edited.CatalogVersion != 2 || edited.PriceVersion != saved.PriceVersion || edited.Version != saved.Version || edited.SKU != saved.SKU {
		t.Errorf("metadata edit changed wrong versions: %+v", edited)
	}
	if _, err := s.SaveProduct(p); !errors.Is(err, ErrConflict) {
		t.Errorf("stale product edit = %v", err)
	}
	edited.Price++
	if _, err := s.SaveProduct(edited); err != nil {
		t.Fatal(err)
	}
	priced := testCatalogProduct(t, s, p.ID)
	if priced.PriceVersion != saved.PriceVersion+1 || priced.CatalogVersion != 3 || priced.Version != saved.Version {
		t.Errorf("price version isolation: %+v", priced)
	}
	priced.SKU = "CHANGED-IDENTITY"
	if _, err := s.SaveProduct(priced); err == nil {
		t.Error("SKU edit accepted")
	}
	if got := testCatalogProduct(t, s, p.ID); got.SKU != saved.SKU {
		t.Error("immutable SKU changed")
	}
	generated := testCreateProduct(t, s, nil)
	if !strings.HasPrefix(generated.SKU, "SHOPDEMO-") || generated.SKU == p.SKU {
		t.Errorf("generated SKU: %q", generated.SKU)
	}
	if err := s.ArchiveProduct(p.ID, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProduct(duplicate); !errors.Is(err, ErrDuplicate) {
		t.Errorf("archived SKU reused = %v", err)
	}
}

func TestProductFieldBoundsAndUnitMetadata(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Product)
	}{
		{"nonzero initial stock", func(p *Product) { p.Stock = 75 }},
		{"empty name", func(p *Product) { p.Name = " " }}, {"long name", func(p *Product) { p.Name = strings.Repeat("n", 81) }},
		{"long description", func(p *Product) { p.Description = strings.Repeat("d", 401) }}, {"zero price", func(p *Product) { p.Price = 0 }}, {"large price", func(p *Product) { p.Price = 1000001 }},
		{"missing category", func(p *Product) { p.CategoryID = 0 }}, {"unknown category", func(p *Product) { p.CategoryID = 99999 }}, {"unknown type", func(p *Product) { p.TypeID = 99999 }},
		{"unknown icon", func(p *Product) { p.Icon = "unknown" }}, {"unknown unit", func(p *Product) { p.SaleUnit = "kg" }},
		{"each basis", func(p *Product) { p.PriceBasis = 1000 }}, {"each step", func(p *Product) { p.QuantityStep = 2 }},
		{"grams basis", func(p *Product) { p.SaleUnit = "g"; p.PriceBasis = 1 }}, {"grams zero step", func(p *Product) { p.SaleUnit = "g"; p.PriceBasis = 1000; p.QuantityStep = 0 }}, {"grams large step", func(p *Product) { p.SaleUnit = "g"; p.PriceBasis = 1000; p.QuantityStep = 1001 }},
		{"long SKU", func(p *Product) { p.SKU = strings.Repeat("S", 41) }}, {"non ASCII SKU", func(p *Product) { p.SKU = "CAFÉ" }}, {"spaced SKU", func(p *Product) { p.SKU = "SKU WITH SPACE" }}, {"reserved SKU", func(p *Product) { p.SKU = "shopdemo-009999" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			before := testCount(t, s, "products")
			p := Product{Name: "Valid", CategoryID: testProduct(t, s, 1).CategoryID, Icon: "apple", Price: 100, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}
			tc.change(&p)
			if _, err := s.SaveProduct(p); err == nil {
				t.Errorf("invalid product accepted: %+v", p)
			}
			if testCount(t, s, "products") != before {
				t.Error("invalid product left data")
			}
		})
	}
	s := newTestStore(t)
	for _, step := range []int64{1, 1000} {
		p := testCreateProduct(t, s, func(p *Product) { p.SaleUnit = "g"; p.PriceBasis = 1000; p.QuantityStep = step; p.Price = 1000000 })
		if p.QuantityStep != step {
			t.Errorf("valid step lost: %+v", p)
		}
	}
}

func TestCatalogConcurrentEditsHaveOneWinner(t *testing.T) {
	s := newTestStore(t)
	p := testProduct(t, s, 1)
	const editors = 8
	results := make(chan error, editors)
	var wg sync.WaitGroup
	for i := range editors {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			edit := p
			edit.Name = fmt.Sprintf("Edit %d", i)
			_, err := s.SaveProduct(edit)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) {
			t.Errorf("competing edit error = %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("%d competing edits won", wins)
	}
	if got := testProduct(t, s, 1); got.CatalogVersion != p.CatalogVersion+1 || got.Stock != p.Stock || got.PriceVersion != p.PriceVersion {
		t.Errorf("concurrent edit result: %+v", got)
	}
}

func TestArchivedProductBlocksSalesButPreservesReceiptsAndRemoval(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	id := testCheckout(t, s, owner.ID)
	receipt := testOrder(t, s, id, owner.ID)
	testCart(t, s, owner.ID, 1, 2)
	before := testBasket(t, s, owner.ID)
	session := testSession(t, s, owner.ID)
	p := testProduct(t, s, 1)
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion+1); !errors.Is(err, ErrConflict) {
		t.Errorf("stale archive = %v", err)
	}
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	archived := testCatalogProduct(t, s, 1)
	if !archived.Archived || archived.CatalogVersion != p.CatalogVersion+1 || archived.Stock != p.Stock {
		t.Errorf("archive: %+v", archived)
	}
	active, err := s.Products("", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range active {
		if p.ID == 1 {
			t.Error("archive remains in storefront")
		}
	}
	if got := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(got, receipt) {
		t.Error("archive changed receipt snapshot")
	}
	if b := testBasket(t, s, owner.ID); b.CanCheckout || len(b.Lines) != 1 {
		t.Errorf("archived basket must stay removable but blocked: %+v", b)
	}
	if err := s.SetCart(owner.ID, 1, 3, false); !errors.Is(err, ErrUnavailable) {
		t.Errorf("adding archive = %v", err)
	}
	if _, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, before.Quote); !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrQuote) {
		t.Errorf("archived stale-quote checkout = %v", err)
	}
	if _, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, testBasket(t, s, owner.ID).Quote); !errors.Is(err, ErrUnavailable) {
		t.Errorf("archived current-quote checkout = %v", err)
	}
	if got := testSession(t, s, owner.ID); got != session {
		t.Error("unavailable checkout changed session")
	}
	if testCount(t, s, "orders") != 1 || testCatalogProduct(t, s, 1).Stock != p.Stock {
		t.Error("unavailable checkout mutated orders or stock")
	}
	if err := s.SetCart(owner.ID, 1, 0, false); err != nil {
		t.Fatalf("remove archived item: %v", err)
	}
	if b := testBasket(t, s, owner.ID); len(b.Lines) != 0 || b.Count != 0 {
		t.Errorf("archived item not removed: %+v", b)
	}
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPicked(id, 1, 1, 1, owner.ID, false); err != nil {
		t.Fatalf("archived receipt cannot be picked: %v", err)
	}
	if err := s.Advance(id, "Picking"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutQuoteChangeRollsBackAndRequiresExplicitNewQuote(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	testCart(t, s, owner.ID, 2, 1)
	session := testSession(t, s, owner.ID)
	shown := testBasket(t, s, owner.ID)
	if shown.Quote == "" {
		t.Fatal("basket snapshot has no quote")
	}
	p := testProduct(t, s, 2)
	p.Price += 125
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	current := testBasket(t, s, owner.ID)
	before, err := s.CatalogProducts()
	if err != nil {
		t.Fatal(err)
	}
	if current.Quote == shown.Quote || current.Total == shown.Total {
		t.Fatal("price edit did not change basket quote and total")
	}
	for _, quote := range []string{shown.Quote, "", "tampered-quote"} {
		if id, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, quote); id != 0 || !errors.Is(err, ErrQuote) {
			t.Errorf("quote %q accepted: %d, %v", quote, id, err)
		}
		if got := testSession(t, s, owner.ID); got != session {
			t.Error("quote rejection changed session")
		}
		if got := testBasket(t, s, owner.ID); !reflect.DeepEqual(got, current) {
			t.Error("quote rejection changed basket")
		}
		after, err := s.CatalogProducts()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Error("quote rejection deducted stock or changed versions")
		}
		if testCount(t, s, "orders") != 0 || testCount(t, s, "order_items") != 0 {
			t.Error("quote rejection left partial order")
		}
	}
	id, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, current.Quote)
	if err != nil {
		t.Fatal(err)
	}
	if o := testOrder(t, s, id, owner.ID); o.Total != current.Total {
		t.Errorf("new quote receipt = %d, want %d", o.Total, current.Total)
	}
	// A successfully committed key replays its original order despite later catalog edits.
	p = testProduct(t, s, 1)
	p.Price++
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, current.Quote)
	if err != nil || replay != id {
		t.Errorf("committed quote replay = %d, %v", replay, err)
	}
}

func TestInventoryOnlyChangesDoNotInvalidateCheckoutQuote(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	b := testBasket(t, s, owner.ID)
	session := testSession(t, s, owner.ID)
	p := testProduct(t, s, 1)
	if err := s.Adjust(1, 2, p.Version, "Restock before checkout"); err != nil {
		t.Fatal(err)
	}
	after := testProduct(t, s, 1)
	if after.Version != p.Version+1 || after.CatalogVersion != p.CatalogVersion || after.PriceVersion != p.PriceVersion {
		t.Errorf("inventory edit crossed version domains: %+v", after)
	}
	if got := testBasket(t, s, owner.ID); got.Quote != b.Quote {
		t.Error("stock-only adjustment invalidated price quote")
	}
	if _, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, b.Quote); err != nil {
		t.Errorf("unchanged quote rejected after restock: %v", err)
	}
}

func TestWeightMetadataIsDeferredNotCountedOrOrderable(t *testing.T) {
	s := newTestStore(t)
	p := testCreateProduct(t, s, func(p *Product) { p.SaleUnit = "g"; p.PriceBasis = 1000; p.QuantityStep = 50 })
	owner := testSession(t, s, "")
	if err := s.Adjust(p.ID, 500, p.Version, "Future weighed inventory"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCart(owner.ID, p.ID, 50, false); !errors.Is(err, ErrUnavailable) {
		t.Errorf("weighted cart accepted = %v", err)
	}
	// Corrupt/legacy cart data must fail safely without treating grams as unit counts.
	testCart(t, s, owner.ID, 1, 2)
	testExec(t, s, `INSERT INTO cart(basket_id,product_id,quantity) VALUES(?,?,?)`, testBasket(t, s, owner.ID).ID, p.ID, 50)
	b := testBasket(t, s, owner.ID)
	if b.CanCheckout || b.Count != 2 {
		t.Errorf("weighted basket counted grams as items or became orderable: %+v", b)
	}
	session := testSession(t, s, owner.ID)
	if _, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, b.Quote); !errors.Is(err, ErrUnavailable) {
		t.Errorf("weighted checkout = %v", err)
	}
	if testCount(t, s, "orders") != 0 || testProduct(t, s, 1).Stock != 22 {
		t.Error("weighted rejection left partial order or stock changes")
	}
	if err := s.SetCart(owner.ID, p.ID, 0, false); err != nil {
		t.Errorf("weighted item cannot be removed: %v", err)
	}
	if got := testBasket(t, s, owner.ID); !got.CanCheckout || got.Count != 2 {
		t.Errorf("basket not restored after removing weighed item: %+v", got)
	}
}

func TestReceiptUseLocksSellingUnitAndPriceBasis(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	id := testCheckout(t, s, owner.ID)
	receipt := testOrder(t, s, id, owner.ID)
	p := testProduct(t, s, 1)
	if err := s.Adjust(p.ID, -p.Stock, p.Version, "Clear stock after receipt"); err != nil {
		t.Fatal(err)
	}
	p = testProduct(t, s, 1)
	before := p
	p.SaleUnit = "g"
	p.PriceBasis = 1000
	p.QuantityStep = 100
	if _, err := s.SaveProduct(p); !errors.Is(err, ErrUnitLocked) {
		t.Errorf("used item changed selling unit = %v", err)
	}
	if got := testProduct(t, s, 1); got != before {
		t.Error("rejected unit conversion mutated product")
	}
	if got := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(got, receipt) {
		t.Error("unit conversion changed historical receipt")
	}
}

func TestProductCodeBoundaryIsReadOnlyAndNotRetailGTIN(t *testing.T) {
	s := newTestStore(t)
	p := testCreateProduct(t, s, func(p *Product) { p.SKU = "CUSTOM-MERCHANT-SKU" })
	local := fmt.Sprintf("SHOPDEMO-%06d", p.ID)
	before, err := s.CatalogProducts()
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"code_128", "Code128"} {
		got, err := s.ResolveProductCode(local, format)
		if err != nil || got.ID != p.ID || got.SKU != p.SKU {
			t.Errorf("local code %s resolved to %+v, %v", format, got, err)
		}
	}
	for _, tc := range []struct{ code, format string }{{"000000000101", "ean_13"}, {"000000000101", "Code128"}, {local, "ean_13"}, {"SHOPDEMO-999999", "code_128"}, {p.SKU, "Code128"}, {"", "Code128"}} {
		if _, err := s.ResolveProductCode(tc.code, tc.format); err == nil {
			t.Errorf("unknown/legacy/unsupported code accepted: %+v", tc)
		}
	}
	after, err := s.CatalogProducts()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || testCount(t, s, "orders") != 0 || testCount(t, s, "adjustments") != 0 {
		t.Error("code lookup mutated catalog/orders/inventory")
	}
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveProductCode(local, "Code128"); !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrNotFound) {
		t.Errorf("archived code is resolvable for new sales: %v", err)
	}
	var legacy string
	if err := s.db.QueryRow(`SELECT raw_value FROM product_codes WHERE product_id=1 AND scheme='legacy_placeholder'`).Scan(&legacy); err != nil || legacy != "000000000101" {
		t.Errorf("legacy placeholder lost leading zeroes: %q, %v", legacy, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM product_codes WHERE product_id=? AND scheme='demo_local'`, p.ID).Scan(&count); err != nil || count != 1 {
		t.Errorf("local identity not retained after archive: %d, %v", count, err)
	}
}

func TestCatalogCreationFailureRollsBackProductCodeAndIdentity(t *testing.T) {
	s := newTestStore(t)
	products, codes := testCount(t, s, "products"), testCount(t, s, "product_codes")
	var next int64
	if err := s.db.QueryRow(`SELECT next_product_id FROM catalog_sequence WHERE id=1`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `CREATE TRIGGER reject_new_local_code BEFORE INSERT ON product_codes WHEN NEW.scheme='demo_local' BEGIN SELECT RAISE(ABORT,'injected identity failure'); END`)
	p := Product{Name: "Atomic catalog product", CategoryID: testProduct(t, s, 1).CategoryID, Icon: "apple", Price: 249, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}
	if id, err := s.SaveProduct(p); err == nil || id != 0 {
		t.Errorf("injected create failure = %d, %v", id, err)
	}
	if testCount(t, s, "products") != products || testCount(t, s, "product_codes") != codes {
		t.Error("failed creation left a product or incomplete identity")
	}
	var after int64
	if err := s.db.QueryRow(`SELECT next_product_id FROM catalog_sequence WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != next {
		t.Errorf("failed creation consumed identity: next before=%d, after=%d", next, after)
	}
	testExec(t, s, `DROP TRIGGER reject_new_local_code`)
	id, err := s.SaveProduct(p)
	if err != nil || id != next {
		t.Errorf("retry after rollback = %d, %v; want next identity %d", id, err, next)
	}
}

func TestCatalogAuditCommitsOnlySuccessfulChanges(t *testing.T) {
	s := newTestStore(t)
	before := testCount(t, s, "catalog_events")
	p := testCreateProduct(t, s, nil)
	if got := testCount(t, s, "catalog_events"); got != before+1 {
		t.Fatalf("product create audit count = %d, want %d", got, before+1)
	}
	original := p
	p.Name = "Audited catalog edit"
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, p.ID)
	if _, err := s.SaveProduct(original); !errors.Is(err, ErrConflict) {
		t.Errorf("stale audit test edit = %v", err)
	}
	if err := s.ArchiveProduct(p.ID, original.CatalogVersion); !errors.Is(err, ErrConflict) {
		t.Errorf("stale archive = %v", err)
	}
	if got := testCount(t, s, "catalog_events"); got != before+2 {
		t.Errorf("stale mutation added audit events: %d", got)
	}
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"category", "type"} {
		id, err := s.SaveTaxonomy(kind, 0, 0, "Audited "+kind)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveTaxonomy(kind, id, 1, "Audited "+kind+" renamed"); err != nil {
			t.Fatal(err)
		}
		if err := s.ArchiveTaxonomy(kind, id, 2); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveTaxonomy(kind, id, 2, "Stale audit rename"); !errors.Is(err, ErrConflict) {
			t.Errorf("archived taxonomy edit = %v", err)
		}
	}
	if got := testCount(t, s, "catalog_events"); got != before+9 {
		t.Errorf("successful catalog audit events = %d, want %d", got, before+9)
	}
}

func TestCatalogAuditFailureRollsBackEveryMutation(t *testing.T) {
	for _, action := range []string{"product create", "product edit", "product archive", "category create", "category edit", "category archive", "type create", "type edit", "type archive", "category restore", "type restore", "product restore"} {
		t.Run(action, func(t *testing.T) {
			s := newTestStore(t)
			p := testCreateProduct(t, s, nil)
			category, err := s.SaveTaxonomy("category", 0, 0, "Audit rollback category")
			if err != nil {
				t.Fatal(err)
			}
			typ, err := s.SaveTaxonomy("type", 0, 0, "Audit rollback type")
			if err != nil {
				t.Fatal(err)
			}
			if action == "product restore" {
				if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
					t.Fatal(err)
				}
				p = testCatalogProduct(t, s, p.ID)
			}
			if action == "category restore" {
				if err := s.ArchiveTaxonomy("category", category, 1); err != nil {
					t.Fatal(err)
				}
			}
			if action == "type restore" {
				if err := s.ArchiveTaxonomy("type", typ, 1); err != nil {
					t.Fatal(err)
				}
			}
			products, err := s.CatalogProducts()
			if err != nil {
				t.Fatal(err)
			}
			categories, err := s.Taxonomies("category", true)
			if err != nil {
				t.Fatal(err)
			}
			types, err := s.Taxonomies("type", true)
			if err != nil {
				t.Fatal(err)
			}
			events, codes := testCount(t, s, "catalog_events"), testCount(t, s, "product_codes")
			productCodes := testProductCodes(t, s, p.ID)
			testExec(t, s, `CREATE TRIGGER reject_catalog_audit BEFORE INSERT ON catalog_events BEGIN SELECT RAISE(ABORT,'injected catalog audit failure'); END`)
			switch action {
			case "product create":
				p.ID = 0
				p.SKU = "AUDIT-ROLLBACK-NEW"
				_, err = s.SaveProduct(p)
			case "product edit":
				p.Name = "Must roll back edit"
				p.Price++
				_, err = s.SaveProduct(p)
			case "product archive":
				err = s.ArchiveProduct(p.ID, p.CatalogVersion)
			case "product restore":
				err = s.RestoreProduct(p.ID, p.CatalogVersion)
			case "category create":
				_, err = s.SaveTaxonomy("category", 0, 0, "Must roll back category")
			case "category edit":
				_, err = s.SaveTaxonomy("category", category, 1, "Must roll back rename")
			case "category archive":
				err = s.ArchiveTaxonomy("category", category, 1)
			case "type create":
				_, err = s.SaveTaxonomy("type", 0, 0, "Must roll back type")
			case "type edit":
				_, err = s.SaveTaxonomy("type", typ, 1, "Must roll back rename")
			case "type archive":
				err = s.ArchiveTaxonomy("type", typ, 1)
			case "category restore":
				err = s.RestoreTaxonomy("category", category, 2)
			case "type restore":
				err = s.RestoreTaxonomy("type", typ, 2)
			}
			if err == nil {
				t.Error("catalog mutation succeeded despite audit failure")
			}
			if after, err := s.CatalogProducts(); err != nil || !reflect.DeepEqual(products, after) {
				t.Errorf("failed audit changed products: %v", err)
			}
			if after, err := s.Taxonomies("category", true); err != nil || !reflect.DeepEqual(categories, after) {
				t.Errorf("failed audit changed categories: %v", err)
			}
			if after, err := s.Taxonomies("type", true); err != nil || !reflect.DeepEqual(types, after) {
				t.Errorf("failed audit changed types: %v", err)
			}
			if action != "product create" {
				if after := testProductCodes(t, s, p.ID); !reflect.DeepEqual(productCodes, after) {
					t.Error("failed audit changed product code archive state")
				}
			}
			if testCount(t, s, "catalog_events") != events || testCount(t, s, "product_codes") != codes {
				t.Error("failed audit left partial event/code state")
			}
		})
	}
}

func TestTaxonomyRestorePreservesIdentityAndArchiveOwnership(t *testing.T) {
	for _, kind := range []string{"category", "type"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			name := "Restore safe " + kind
			id, err := s.SaveTaxonomy(kind, 0, 0, name)
			if err != nil {
				t.Fatal(err)
			}
			p := testCreateProduct(t, s, func(p *Product) {
				if kind == "category" {
					p.CategoryID = id
				} else {
					p.TypeID = id
				}
			})
			if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
				t.Fatal(err)
			}
			if err := s.ArchiveTaxonomy(kind, id, 1); err != nil {
				t.Fatal(err)
			}
			archived := testTaxonomy(t, s, kind, id)
			events := testCount(t, s, "catalog_events")
			if err := s.RestoreTaxonomy(kind, id, 1); !errors.Is(err, ErrConflict) {
				t.Errorf("stale restore = %v", err)
			}
			if got := testTaxonomy(t, s, kind, id); got != archived || testCount(t, s, "catalog_events") != events {
				t.Error("stale restore mutated taxonomy/audit")
			}
			if _, err := s.SaveTaxonomy(kind, 0, 0, strings.ToUpper(name)); !errors.Is(err, ErrDuplicate) {
				t.Errorf("archived name lost reservation = %v", err)
			}
			if err := s.RestoreTaxonomy(kind, id, archived.Version); err != nil {
				t.Fatal(err)
			}
			restored := testTaxonomy(t, s, kind, id)
			if restored.ID != id || restored.Name != name || restored.Archived || restored.Version != archived.Version+1 {
				t.Errorf("restore changed identity or version: %+v", restored)
			}
			if testCount(t, s, "catalog_events") != events+1 {
				t.Error("restore missing committed audit event")
			}
			if !testCatalogProduct(t, s, p.ID).Archived {
				t.Error("restoring taxonomy silently restored its product")
			}
			if _, err := s.SaveTaxonomy(kind, 0, 0, name); !errors.Is(err, ErrDuplicate) {
				t.Errorf("restored identity reused: %v", err)
			}
			if err := s.RestoreTaxonomy(kind, id, restored.Version); !errors.Is(err, ErrConflict) {
				t.Errorf("restoring active taxonomy = %v", err)
			}
			testCreateProduct(t, s, func(p *Product) {
				if kind == "category" {
					p.CategoryID = id
				} else {
					p.TypeID = id
				}
			})
		})
	}
}

func TestCatalogAuditReadIsBoundedAndUsesSnapshots(t *testing.T) {
	s := newTestStore(t)
	var last int64
	for i := range 25 {
		id, err := s.SaveTaxonomy("category", 0, 0, fmt.Sprintf("Audit category %02d", i))
		if err != nil {
			t.Fatal(err)
		}
		last = id
	}
	events, err := s.CatalogEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 20 {
		t.Fatalf("recent audit length = %d, want bounded20", len(events))
	}
	if events[0].Kind != "category" || events[0].Action != "create" || events[0].EntityID != last || events[0].Name != "Audit category 24" || events[0].Details == "" || events[0].Created == "" {
		t.Errorf("newest audit entry: %+v", events[0])
	}
	if _, err := s.SaveTaxonomy("category", last, 1, "Renamed audit category"); err != nil {
		t.Fatal(err)
	}
	events, err = s.CatalogEvents()
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Action != "edit" || events[0].Name != "Renamed audit category" || events[1].Name != "Audit category 24" {
		t.Errorf("audit names joined mutable catalog instead of snapshots: %+v", events[:2])
	}
}

func testProductCodes(t *testing.T, s *Store, id int64) []ProductCode {
	t.Helper()
	rows, err := s.db.Query(`SELECT id,product_id,scheme,raw_value,normalized_value,symbology,archived FROM product_codes WHERE product_id=? ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var codes []ProductCode
	for rows.Next() {
		var code ProductCode
		if err := rows.Scan(&code.ID, &code.ProductID, &code.Scheme, &code.RawValue, &code.NormalizedValue, &code.Symbology, &code.Archived); err != nil {
			t.Fatal(err)
		}
		codes = append(codes, code)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return codes
}

func TestProductRestorePreservesIdentityQuotesHistoryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore.db")
	s := openTestStore(t, path)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	oldOrder := testCheckout(t, s, owner.ID)
	receipt := testOrder(t, s, oldOrder, owner.ID)
	testCart(t, s, owner.ID, 1, 2)
	session := testSession(t, s, owner.ID)
	oldBasket := testBasket(t, s, owner.ID)
	p := testProduct(t, s, 1)
	codes := testProductCodes(t, s, p.ID)
	unrelated := testProduct(t, s, 2)
	if err := s.ArchiveProduct(unrelated.ID, unrelated.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	unrelated = testCatalogProduct(t, s, 2)
	unrelatedCodes := testProductCodes(t, s, 2)
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	archived := testCatalogProduct(t, s, 1)
	events := testCount(t, s, "catalog_events")
	if err := s.RestoreProduct(p.ID, p.CatalogVersion); !errors.Is(err, ErrConflict) {
		t.Errorf("stale product restore = %v", err)
	}
	if got := testCatalogProduct(t, s, p.ID); got != archived || testCount(t, s, "catalog_events") != events {
		t.Error("stale restore changed product/audit")
	}
	if err := s.RestoreProduct(p.ID, archived.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	restored := testProduct(t, s, p.ID)
	want := p
	want.CatalogVersion = archived.CatalogVersion + 1
	want.PriceVersion = archived.PriceVersion + 1
	if restored != want {
		t.Errorf("restore altered product/stock identity: got %+v, want %+v", restored, want)
	}
	if after := testProductCodes(t, s, p.ID); !reflect.DeepEqual(after, codes) {
		t.Errorf("restore replaced code identities: got %+v, want %+v", after, codes)
	}
	if got := testCatalogProduct(t, s, 2); got != unrelated || !reflect.DeepEqual(testProductCodes(t, s, 2), unrelatedCodes) {
		t.Error("product restore changed another archived product or its codes")
	}
	if testCount(t, s, "catalog_events") != events+1 {
		t.Error("successful restore omitted audit event")
	}
	if err := s.RestoreProduct(p.ID, restored.CatalogVersion); !errors.Is(err, ErrConflict) {
		t.Errorf("active product restored again: %v", err)
	}
	if got := testOrder(t, s, oldOrder, owner.ID); !reflect.DeepEqual(got, receipt) {
		t.Error("restore changed historical receipt")
	}
	if _, err := s.ResolveProductCode(fmt.Sprintf("SHOPDEMO-%06d", p.ID), "Code128"); err != nil {
		t.Errorf("restored local code not resolvable: %v", err)
	}
	current := testBasket(t, s, owner.ID)
	if current.Quote == oldBasket.Quote || current.Total != oldBasket.Total || !current.CanCheckout {
		t.Errorf("restored basket must require new approval for unchanged amount: %+v", current)
	}
	if _, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, oldBasket.Quote); !errors.Is(err, ErrQuote) {
		t.Errorf("pre-archive quote accepted after restore: %v", err)
	}
	if got := testProduct(t, s, p.ID); got.Stock != p.Stock || testCount(t, s, "orders") != 1 {
		t.Error("old quote mutated stock/orders")
	}
	if got := testSession(t, s, owner.ID); got != session {
		t.Error("old quote changed session")
	}
	if _, err := s.Checkout(owner.ID, session.CheckoutKey, session.Revision, current.Quote); err != nil {
		t.Errorf("explicit restored quote checkout failed: %v", err)
	}
	finalProduct := testProduct(t, s, p.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if got := testProduct(t, reopened, p.ID); got != finalProduct || !reflect.DeepEqual(testProductCodes(t, reopened, p.ID), codes) {
		t.Error("restored product/code identity did not persist")
	}
	if got := testOrder(t, reopened, oldOrder, owner.ID); !reflect.DeepEqual(got, receipt) {
		t.Error("restart changed historical receipt")
	}
	if !testCatalogProduct(t, reopened, 2).Archived {
		t.Error("restart restored unrelated product")
	}
}

func TestProductRestoreRequiresActiveCategoryAndType(t *testing.T) {
	for _, kind := range []string{"category", "type"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			id, err := s.SaveTaxonomy(kind, 0, 0, "Inactive restore "+kind)
			if err != nil {
				t.Fatal(err)
			}
			p := testCreateProduct(t, s, func(p *Product) {
				if kind == "category" {
					p.CategoryID = id
				} else {
					p.TypeID = id
				}
			})
			if err := s.Adjust(p.ID, 3, p.Version, "Stock for recovery"); err != nil {
				t.Fatal(err)
			}
			if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
				t.Fatal(err)
			}
			if err := s.ArchiveTaxonomy(kind, id, 1); err != nil {
				t.Fatal(err)
			}
			before := testCatalogProduct(t, s, p.ID)
			codes := testProductCodes(t, s, p.ID)
			events := testCount(t, s, "catalog_events")
			if err := s.RestoreProduct(p.ID, before.CatalogVersion); !errors.Is(err, ErrTaxonomyInactive) {
				t.Errorf("restore under inactive %s = %v", kind, err)
			}
			if got := testCatalogProduct(t, s, p.ID); got != before || !reflect.DeepEqual(codes, testProductCodes(t, s, p.ID)) || testCount(t, s, "catalog_events") != events {
				t.Error("blocked restore changed product/codes/audit")
			}
			if err := s.RestoreTaxonomy(kind, id, 2); err != nil {
				t.Fatal(err)
			}
			if !testCatalogProduct(t, s, p.ID).Archived {
				t.Error("taxonomy restore implicitly recovered product")
			}
			if err := s.RestoreProduct(p.ID, before.CatalogVersion); err != nil {
				t.Fatalf("explicit recovery after active taxonomy: %v", err)
			}
			if got := testProduct(t, s, p.ID); got.Stock != 3 || got.SKU != before.SKU || got.ID != p.ID {
				t.Errorf("recovery lost stock/identity: %+v", got)
			}
		})
	}
}
