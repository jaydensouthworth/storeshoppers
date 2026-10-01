package shop

import (
	"errors"
	"fmt"
)

var ErrConflict = errors.New("This changed since you opened it. Refresh and try again.")
var ErrStock = errors.New("Not enough stock for this order. Review your basket and try again.")
var ErrEmpty = errors.New("Add something to your basket first.")
var ErrInvalid = errors.New("Please check the submitted values.")
var ErrIncomplete = errors.New("Pick every required item before marking this order ready.")
var ErrNotFound = errors.New("That item could not be found.")

type Product struct {
	ID                                         int64
	Name, Description, Category, Barcode, Icon string
	Price, Stock, Version                      int64
}
type Session struct {
	ID, CSRF, CheckoutKey  string
	Revision, ManagerUntil int64
}
type CartLine struct {
	Product            Product
	Quantity, Subtotal int64
}
type Basket struct {
	Lines        []CartLine
	Total, Count int64
	CanCheckout  bool
}
type OrderItem struct {
	Name                           string
	Price, Quantity, Subtotal      int64
	ProductID, Picked, PickVersion int64
}
type Order struct {
	ID                         int64
	Reference, Status, Created string
	Total                      int64
	Items                      []OrderItem
	PickedCount, RequiredCount int64
	AllPicked                  bool
}
type Adjustment struct {
	Product, Reason, Created string
	Delta                    int64
}

func Money(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) }
