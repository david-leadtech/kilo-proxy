package main

import (
	"fmt"
	"math/big"
	"regexp"
)

var accountMoneyPattern = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)

// A balance may be negative. Keep decimal arithmetic exact, and never turn a
// positive or negative sub-cent balance into an apparently available $0.00.
func accountBalanceAmount(value *string) string {
	if value == nil || !accountMoneyPattern.MatchString(*value) {
		return "—"
	}
	amount, ok := new(big.Rat).SetString(*value)
	if !ok {
		return "—"
	}
	sign := ""
	if amount.Sign() < 0 {
		sign = "−"
		amount.Abs(amount)
	}
	if amount.Sign() > 0 && amount.Cmp(big.NewRat(1, 100)) < 0 {
		return sign + "<$0.01"
	}
	amount.Mul(amount, big.NewRat(100, 1))
	amount.Add(amount, big.NewRat(1, 2))
	cents := new(big.Int).Quo(amount.Num(), amount.Denom())
	dollars, remainder := new(big.Int), new(big.Int)
	dollars.QuoRem(cents, big.NewInt(100), remainder)
	return fmt.Sprintf("%s$%s.%02d", sign, dollars.String(), remainder.Int64())
}
