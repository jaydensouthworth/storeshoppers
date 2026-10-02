package shop

import (
	"errors"
	"math"
)

const maxGrams = int64(100000)
const maxGramStock = int64(1000000)

var ErrZeroEstimate = errors.New("The basket estimate must be at least one cent. Increase the requested weight or add another item before reserving.")
var ErrWeightReview = errors.New("Review the actual weight and stock consequences before confirming.")

func quantityLimit(unit string) int64 {
	if unit == "g" {
		return maxGrams
	}
	return 99
}
func stockLimit(unit string) int64 {
	if unit == "g" {
		return maxGramStock
	}
	return 10000
}
func validQuantity(unit string, quantity, step int64, zero bool) bool {
	return (unit == "each" || unit == "g") && quantity >= 0 && (zero || quantity > 0) && quantity <= quantityLimit(unit) && step > 0 && quantity%step == 0
}

// lineAmount rounds only once for the complete line, never per gram. Bounds
// are checked before multiplication; this also refuses malformed snapshots.
func lineAmount(quantity, price, basis int64, unit string) (int64, error) {
	if price < 1 || price > 1000000 || quantity < 0 || quantity > quantityLimit(unit) ||
		!((unit == "each" && basis == 1) || (unit == "g" && basis == 1000)) {
		return 0, ErrInvalid
	}
	if quantity > (math.MaxInt64-basis/2)/price {
		return 0, ErrInvalid
	}
	return (quantity*price + basis/2) / basis, nil
}
func addAmount(total, line int64) (int64, error) {
	if total < 0 || line < 0 || line > math.MaxInt64-total {
		return 0, ErrInvalid
	}
	return total + line, nil
}
func (p Product) QuantityLimit() int64   { return quantityLimit(p.SaleUnit) }
func (p Product) StockLimit() int64      { return stockLimit(p.SaleUnit) }
func (i OrderItem) QuantityLimit() int64 { return quantityLimit(i.SaleUnit) }
func unitLabel(unit string) string {
	if unit == "g" {
		return "g"
	}
	return "units"
}
func (p Product) UnitLabel() string   { return unitLabel(p.SaleUnit) }
func (i OrderItem) UnitLabel() string { return unitLabel(i.SaleUnit) }
func (b Basket) CountLabel() string {
	if b.HasWeight {
		if b.Count == 1 {
			return "product line"
		}
		return "product lines"
	}
	if b.Count == 1 {
		return "item"
	}
	return "items"
}
func (o Order) ProgressLabel() string {
	if o.HasWeight {
		return "product lines"
	}
	return "units"
}
func (i WorkingOrderItem) Complete() bool {
	if i.SaleUnit == "g" {
		return i.Measured && i.Unavailable == 0 && i.Cancelled == 0
	}
	return i.Quantity > 0 && i.Picked == i.Quantity
}
func summarizeWorking(o *Order, items []WorkingOrderItem) error {
	o.WorkingTotal, o.PickedCount, o.RequiredCount, o.PickedTotal = 0, 0, 0, 0
	o.HasWeight = false
	for _, i := range items {
		if i.SaleUnit == "g" {
			o.HasWeight = true
		}
	}
	for _, i := range items {
		var err error
		o.WorkingTotal, err = addAmount(o.WorkingTotal, i.Subtotal)
		if err != nil {
			return err
		}
		pickedAmount, err := lineAmount(i.Picked, i.Price, i.PriceBasis, i.SaleUnit)
		if err != nil {
			return err
		}
		o.PickedTotal, err = addAmount(o.PickedTotal, pickedAmount)
		if err != nil {
			return err
		}
		if o.HasWeight {
			o.RequiredCount++
			if i.Complete() {
				o.PickedCount++
			}
		} else {
			o.PickedCount += i.Picked
			o.RequiredCount += i.Quantity
		}
	}
	setOrderProgress(o)
	return nil
}
