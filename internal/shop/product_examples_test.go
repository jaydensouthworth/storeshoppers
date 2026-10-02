package shop

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func productExamplesStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog-without-examples.db")
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacyPromotionsV6)
	// Migration 3 always creates this row, including for an empty catalog.
	migrationExec(t, db, `INSERT INTO catalog_sequence VALUES(1,1)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return openTestStore(t, path)
}

func productExamplesQuote(t *testing.T, s *Store) string {
	t.Helper()
	before := fingerprintTest(t, s.db)
	w, err := s.ProductExamples()
	if err != nil || w.AlreadyAdded || len(w.Quote) != 64 || len(w.Examples) != 2 {
		t.Fatalf("example review: workspace=%+v error=%v", w, err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("reviewing examples mutated the catalog")
	}
	return w.Quote
}

func TestProductExamplesAuditProvenanceAndReplayPreserveLaterWork(t *testing.T) {
	s := productExamplesStore(t)
	quote, key := productExamplesQuote(t, s), token()
	if err := s.CreateProductExamples(key, quote); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"products": 2, "product_details": 2, "product_example_products": 2, "product_detail_commands": 1, "product_codes": 2, "adjustments": 2, "catalog_events": 5} {
		if got := testCount(t, s, table); got != want {
			t.Errorf("%s rows=%d, want %d", table, got, want)
		}
	}
	w, err := s.ProductExamples()
	if err != nil || !w.AlreadyAdded || w.Quote != "" || len(w.Examples) != 2 {
		t.Fatalf("installed examples: workspace=%+v error=%v", w, err)
	}
	for _, example := range w.Examples {
		p, d := example.Product, example.Details
		if p.ID < 1 || p.Stock <= 0 || p.Version != 2 || p.CatalogVersion != 1 || p.PriceVersion != 1 || p.SaleUnit != "each" || p.PriceBasis != 1 || p.QuantityStep != 1 || p.Archived {
			t.Errorf("example inventory or version domains=%+v", p)
		}
		if d.ProductID != p.ID || d.Kind != "nonfood" || d.Nutrition != nil || d.Body == "" || d.PackageLabel == "" {
			t.Errorf("example presentation=%+v", d)
		}
		var delta, count int64
		if err := s.db.QueryRow(`SELECT COUNT(*),SUM(delta) FROM adjustments WHERE product_id=? AND sale_unit='each'`, p.ID).Scan(&count, &delta); err != nil || count != 1 || delta != p.Stock {
			t.Errorf("example stock audit: count=%d delta=%d error=%v", count, delta, err)
		}
		var audit string
		if err := s.db.QueryRow(`SELECT details FROM catalog_events WHERE kind='product' AND entity_id=? AND action='create'`, p.ID).Scan(&audit); err != nil || !strings.Contains(audit, p.SKU) || !strings.Contains(audit, d.PackageLabel) || !strings.Contains(audit, d.Body) {
			t.Errorf("example catalog audit=%q error=%v", audit, err)
		}
	}
	p := w.Examples[0].Product
	d := w.Examples[0].Details
	p.Name, d.Body = "Manager's retained product name", "Manager's retained package story"
	if _, err := s.SaveProductWithDetails(p, d); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, p.ID)
	if err := s.Adjust(p.ID, -3, p.Version, "A later inventory adjustment"); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	for _, retry := range []struct {
		key, quote string
		want       error
	}{{key, quote, nil}, {key, strings.Repeat("f", 64), ErrConflict}, {token(), quote, ErrConflict}} {
		if err := s.CreateProductExamples(retry.key, retry.quote); !errors.Is(err, retry.want) {
			t.Errorf("example retry=%v, want %v", err, retry.want)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("example retry restored, restocked, duplicated, or rewrote later manager work")
		}
	}
	w, err = s.ProductExamples()
	if err != nil || !w.AlreadyAdded || !w.Examples[0].Product.Archived || w.Examples[0].Product.Name != p.Name || !reflect.DeepEqual(w.Examples[0].Details, d) {
		t.Fatalf("installed review lost retained current/archived state: workspace=%+v error=%v", w, err)
	}
}

func TestProductExamplesRefuseReservedSKUAndArchivedRequiredLabels(t *testing.T) {
	for _, target := range []string{"active SKU", "archived SKU", "category", "deodorant type", "shaving type"} {
		t.Run(target, func(t *testing.T) {
			s := productExamplesStore(t)
			quote := productExamplesQuote(t, s)
			kind, name := "category", "Personal care"
			if strings.HasSuffix(target, "type") {
				kind, name = "type", "Deodorant"
				if target == "shaving type" {
					name = "Shaving"
				}
			}
			id, err := s.SaveTaxonomy(kind, 0, 0, name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(target, "SKU") {
				id, err := s.SaveProduct(Product{SKU: "demo-deodorant-75g", Name: "Manager's own deodorant", CategoryID: id, Icon: "apple", Price: 999, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1})
				if err != nil {
					t.Fatal(err)
				}
				if target == "archived SKU" {
					if err := s.ArchiveProduct(id, 1); err != nil {
						t.Fatal(err)
					}
				}
			} else if err := s.ArchiveTaxonomy(kind, id, 1); err != nil {
				t.Fatal(err)
			}
			before := fingerprintTest(t, s.db)
			if _, err := s.ProductExamples(); !errors.Is(err, ErrProductExamples) {
				t.Errorf("conflicting preview=%v, want ErrProductExamples", err)
			}
			if err := s.CreateProductExamples(token(), quote); !errors.Is(err, ErrProductExamples) {
				t.Errorf("conflicting import=%v, want ErrProductExamples", err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("conflicting examples altered the catalog, taxonomy, audits, or sequence")
			}
		})
	}
}

func TestProductExamplesRequireFreshReviewAndCompleteProvenance(t *testing.T) {
	s := productExamplesStore(t)
	quote, key := productExamplesQuote(t, s), token()
	id, err := s.SaveTaxonomy("type", 0, 0, "Deodorant")
	if err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	if err := s.CreateProductExamples(key, quote); !errors.Is(err, ErrConflict) {
		t.Fatalf("review before a relevant label changed=%v, want ErrConflict", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("stale review partially installed examples")
	}
	quote = productExamplesQuote(t, s)
	if err := s.CreateProductExamples(key, quote); err != nil {
		t.Fatal(err)
	}
	w, err := s.ProductExamples()
	if err != nil || w.Examples[0].Product.TypeID != id {
		t.Fatalf("reviewed active label was not reused: workspace=%+v error=%v", w, err)
	}
	// Incomplete persisted provenance must be a conflict, never permission to
	// regenerate examples, even if the retained product later becomes archived.
	testExec(t, s, `DELETE FROM product_example_products WHERE example_key='DEMO-RAZORS-3PK'`)
	before = fingerprintTest(t, s.db)
	if _, err := s.ProductExamples(); !errors.Is(err, ErrProductExamples) {
		t.Errorf("partial provenance preview=%v, want ErrProductExamples", err)
	}
	if err := s.CreateProductExamples(token(), quote); !errors.Is(err, ErrProductExamples) {
		t.Errorf("partial provenance import=%v, want ErrProductExamples", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("partial provenance caused repair, duplication, or restoration")
	}
}

func TestProductExamplesLateFailuresRollbackWholeInstallation(t *testing.T) {
	for _, failure := range []struct{ name, trigger string }{
		{"second product details", `CREATE TRIGGER reject_example BEFORE INSERT ON product_details WHEN NEW.product_id=2 BEGIN SELECT RAISE(ABORT,'injected example failure'); END`},
		{"second product catalog audit", `CREATE TRIGGER reject_example BEFORE INSERT ON catalog_events WHEN NEW.kind='product' AND NEW.entity_id=2 BEGIN SELECT RAISE(ABORT,'injected example failure'); END`},
		{"second product stock audit", `CREATE TRIGGER reject_example BEFORE INSERT ON adjustments WHEN NEW.product_id=2 BEGIN SELECT RAISE(ABORT,'injected example failure'); END`},
		{"second product provenance", `CREATE TRIGGER reject_example BEFORE INSERT ON product_example_products WHEN NEW.example_key='DEMO-RAZORS-3PK' BEGIN SELECT RAISE(ABORT,'injected example failure'); END`},
		{"final replay record", `CREATE TRIGGER reject_example BEFORE INSERT ON product_detail_commands BEGIN SELECT RAISE(ABORT,'injected example failure'); END`},
	} {
		t.Run(failure.name, func(t *testing.T) {
			s := productExamplesStore(t)
			quote, key := productExamplesQuote(t, s), token()
			testExec(t, s, failure.trigger)
			before := fingerprintTest(t, s.db)
			if err := s.CreateProductExamples(key, quote); err == nil || !strings.Contains(err.Error(), "injected example failure") {
				t.Fatalf("late installation failure=%v", err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("late failure retained a product, stock, label, details, audit, provenance, command, or sequence increment")
			}
			testExec(t, s, `DROP TRIGGER reject_example`)
			if err := s.CreateProductExamples(key, quote); err != nil {
				t.Fatalf("same reviewed request could not retry after rollback: %v", err)
			}
			if testCount(t, s, "products") != 2 || testCount(t, s, "product_detail_commands") != 1 {
				t.Fatal("retried installation did not create exactly one complete example set")
			}
		})
	}
}

func TestProductExamplesConcurrentCommandsCreateOneInstallation(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		t.Run(map[bool]string{true: "same command", false: "separate reviewed commands"}[sameKey], func(t *testing.T) {
			s := productExamplesStore(t)
			quote, key := productExamplesQuote(t, s), token()
			keys := []string{key, key}
			if !sameKey {
				keys[1] = token()
			}
			var wg sync.WaitGroup
			results := make(chan error, len(keys))
			for _, key := range keys {
				wg.Add(1)
				go func(key string) {
					defer wg.Done()
					results <- s.CreateProductExamples(key, quote)
				}(key)
			}
			wg.Wait()
			close(results)
			successes, conflicts := 0, 0
			for err := range results {
				if err == nil {
					successes++
				} else if errors.Is(err, ErrConflict) {
					conflicts++
				} else {
					t.Errorf("concurrent example command=%v", err)
				}
			}
			if sameKey && (successes != 2 || conflicts != 0) || !sameKey && (successes != 1 || conflicts != 1) {
				t.Errorf("concurrent command results: successes=%d conflicts=%d", successes, conflicts)
			}
			if testCount(t, s, "products") != 2 || testCount(t, s, "adjustments") != 2 || testCount(t, s, "product_detail_commands") != 1 {
				t.Fatal("concurrent commands duplicated examples, stock, or replay history")
			}
		})
	}
}
