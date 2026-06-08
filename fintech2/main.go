/**

Design a stdlib-only Money representation for a fintech backend that must safely handle multiple currencies (e.g., EUR 2 dp, USD 2 dp, JPY 0 dp), support JSON I/O, and be usable in maps and channels.

**/

package main

import (
	"fmt"
)

// ISO 4217 codes for currency
var currencyScale = map[string]int{"EUR": 2, "USD": 2, "JYP": 0}

type Money struct {
	Amount   int64  `json:"value"` // without comma, base 10, including decimals
	Currency string `json:"currency"`
}

func NewMoney(cur string, minor int64) (*Money, error) {
	// Validation of valid currency ...
	if sc, ok := currencyScale[cur]; !ok {
		return nil, fmt.Errorf("Currency %s not listed as valid in currency scale", cur)
	}

	return &Money{
		Amount:   minor,
		Currency: cur,
	}, nil
}

func ParseMoney(cur string, major int64, frac int) (Money, error) {
	// validate frac fits into scale
	if frac < 0 {
		return Money(), fmt.Errorf("could not parse: invalid frac of money: %d is negative", frac)
	}
	if frac > 0 {
		sc, ok := currencyScale[cur]
		if !ok {
			return Money{}, fmt.Errorf("could not parse: currency %s not listed as valid in currency scale", cur)
		}
		dp := int(math.Floor(math.Log10(float64(n)))) + 1
		if dp > sc {
			return Money{}, fmt.Error("could not parse: decimal positions longer than allowed in scale")
		}
	}

	return Money {
		Amount: major*10*dp + int64(frac),
		Currency: cur,
	}, nil
}

func (m Money) String() string {
	dp := int(math.Floor(math.Log10(float64(n)))) + 1
	major := int(math.Floor(m.Amount/10^dp))
	dec := int(m.Amount%10^dp)
	
	return fmt.Sprintf("%s %d,%d", m.Currency, major, dec)
}

func (m Money) Add(b Money) (Money, error) {
	// TODO: check if different currency or result would be too big for int64; return error in this case
	return Money{
		Amount: m.Amount+b.Amount,
		Currency: m.Currency,
	}, nil
}

func (m Money) Sub(b Money) (Money, error) {
	// TODO: analog implementation to Add
	return Money{}, nil
}

// Cmp compares the amount of the two Money
// -1 for left is smaller, 0 if they are equal, 1 if right is bigger
func (m Money) Cmp(b Money) (int, error) {
	// TODO: check before for same currency
	switch true {
		m.Amount < b.Amount:
			return -1
		m.Amount == b.Amount:
			return 0
		m.Amount > b.Amount:
			return 1
	}
	return 0, fmt.Error("unknown error while comparing money")
}

func main() {

}
