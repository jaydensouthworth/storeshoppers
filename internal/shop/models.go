package shop

import (
	"errors"
	"fmt"
)

var ErrPickedDisposition = errors.New("Choose what happened to the quantity being removed: returned and available to sell, or unavailable/damaged.")
var ErrOrderQuote = errors.New("Catalog choices or prices changed. Review the refreshed choices and recorded prices, then submit this manager change again.")
var ErrUseReady = errors.New("All working units are already picked. Use Mark ready and Confirm collected to finish the normal collection flow.")
var ErrTerminal = errors.New("This order is ready or closed. Its final receipt and stock disposition cannot be changed or reopened; a separate amendment/refund workflow is not supported in this demo.")
var ErrStockCapacity = errors.New("Returning this stock would exceed the product’s stock limit. Reconcile physical inventory first, or choose write-off only for stock that cannot be resold.")
var ErrConflict = errors.New("This changed since you opened it. Refresh and try again.")
var ErrHold = errors.New("Your stock hold expired or needs review. Review and reserve your basket for 15 minutes before checkout.")
var ErrStock = errors.New("Not enough stock for this order. Review your basket and try again.")
var ErrEmpty = errors.New("Add something to your basket first.")
var ErrInvalid = errors.New("Please check the submitted values.")
var ErrAttention = errors.New("Resolve the manager hold before marking ready or finishing this order. Review the hold below.")
var ErrIncomplete = errors.New("Pick every required item before marking this order ready.")
var ErrUnavailable = errors.New("This product is archived or not available to order. Remove it from your basket to continue.")
var ErrQuote = errors.New("Your basket prices or availability changed. Review the current basket, then place your order again.")
var ErrReferenced = errors.New("Reassign or archive active products before archiving this category or product type.")
var ErrDuplicate = errors.New("That name or identifier is already in use, including archived records.")
var ErrUnitLocked = errors.New("Selling unit and price basis cannot change after an order uses this product or while stock remains. Create a new product instead.")
var ErrTaxonomyInactive = errors.New("Restore this product’s department and product type before restoring the product.")
var ErrNotFound = errors.New("That item could not be found.")

type Product struct {
	ImageHash                                                      string
	PricingBoundary                                                int64
	SalePrice, PromotionID, PromotionVersion, SaleStarts, SaleEnds int64
	Featured                                                       bool
	FeatureVersion                                                 int64
	ID                                                             int64
	Name, Description, Category, Barcode, Icon                     string
	Reserved                                                       int64
	Price, Stock, Version                                          int64
	SKU, ProductType, SaleUnit                                     string
	CategoryID, TypeID                                             int64
	Archived                                                       bool
	CatalogVersion, PriceVersion                                   int64
	PriceBasis, QuantityStep                                       int64
}
type Taxonomy struct {
	ID, Version int64
	Name        string
	Archived    bool
}
type ProductCode struct {
	ID, ProductID                                int64
	Scheme, RawValue, NormalizedValue, Symbology string
	Archived                                     bool
}
type Session struct {
	ID, CSRF, CheckoutKey  string
	Revision, ManagerUntil int64
}
type CartLine struct {
	Product                      Product
	Quantity, Subtotal, Reserved int64
}
type Basket struct {
	HasWeight              bool
	ID, Label, HoldLabel   string
	Synthetic, NeedsReview bool
	HoldUntil, Revision    int64
	Lines                  []CartLine
	Total, Count           int64
	CanCheckout            bool
	Quote                  string
}
type OrderItem struct {
	Name                           string
	Price, Quantity, Subtotal      int64
	ProductID, Picked, PickVersion int64
	SKU, SaleUnit                  string
	PriceBasis, QuantityStep       int64
}
type WorkingOrderItem struct {
	OrderItem
	LineID, Unavailable, Cancelled int64
	Allocated                      int64
	Measured                       bool
}
type OrderEvent struct {
	Visibility                       string
	Action, Reason, Details, Created string
}
type Order struct {
	AttentionReason                                string
	AttentionSince                                 int64
	Held                                           bool
	HasWeight                                      bool
	Assignment                                     *ShopperAssignment
	WorkingSteps                                   map[int64]int64
	WorkingPrices                                  map[int64]int64
	Version, WorkingTotal, FinalTotal, PickedTotal int64
	Finalized                                      bool
	CompletionKind                                 string
	WorkingItems                                   []WorkingOrderItem
	Events                                         []OrderEvent
	Instructions                                   string
	Percent                                        int64
	ID                                             int64
	Reference, Status, Created                     string
	Total                                          int64
	Items                                          []OrderItem
	PickedCount, RequiredCount                     int64
	AllPicked                                      bool
}
type Adjustment struct {
	Product, Reason, Created string
	Delta                    int64
	SaleUnit                 string
}

func Money(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) }

// CatalogEvent records a successful manager catalog command in its transaction.
// The shared demo gate has no named users; this is a change log, not identity proof.
type CatalogEvent struct {
	Kind, Action, Name, Details, Created string
	EntityID                             int64
}

// BasketEvent uses public basket identifiers, never authentication tokens.
type BasketEvent struct {
	BasketID, Label, Action, Reason, Details, Created string
}
