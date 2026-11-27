package inbound

import (
	"errors"
	"fmt"
	"math/big"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/roweval"
)

func convertTemperature(row heatingRow, text, unit string) (semreg.Decimal, error) {
	number, err := parseDecimal(text)
	if err != nil {
		return semreg.Decimal{}, err
	}
	switch unit {
	case "degC", "°C":
		if row.Affine == nil {
			return semreg.Decimal{}, errors.New("inbound: temperature row has no affine")
		}
		return applyAffine(number, *row.Affine)
	case "K":
		return number, nil
	default:
		return semreg.Decimal{}, fmt.Errorf("inbound: temperature unit %q is not degC or K", unit)
	}
}

func applyAffine(value semreg.Decimal, affine roweval.Affine) (semreg.Decimal, error) {
	if affine.Den == 0 {
		return semreg.Decimal{}, errors.New("inbound: affine denominator is zero")
	}
	if affine.ExponentRow != "" {
		return semreg.Decimal{}, errors.New("inbound: an exponent row is not a heating temperature")
	}
	left, err := decimalRat(value)
	if err != nil {
		return semreg.Decimal{}, err
	}
	left.Mul(left, big.NewRat(affine.Num, affine.Den))
	if affine.Add.Coefficient != "" {
		add, err := decimalRat(affine.Add)
		if err != nil {
			return semreg.Decimal{}, err
		}
		left.Add(left, add)
	}
	return canonicalLabeled(left, "temperature")
}

func scaleDecimal(text string, num, den int64) (semreg.Decimal, error) {
	number, err := parseDecimal(text)
	if err != nil {
		return semreg.Decimal{}, err
	}
	rat, err := decimalRat(number)
	if err != nil {
		return semreg.Decimal{}, err
	}
	if den == 0 {
		return semreg.Decimal{}, errors.New("inbound: scale denominator is zero")
	}
	rat.Mul(rat, big.NewRat(num, den))
	return canonicalLabeled(rat, "quantity")
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func parseDecimal(text string) (semreg.Decimal, error) {
	rat, ok := new(big.Rat).SetString(text)
	if !ok {
		return semreg.Decimal{}, fmt.Errorf("inbound: %q is not a decimal", text)
	}
	return canonicalDecimal(rat)
}

func decimalRat(value semreg.Decimal) (*big.Rat, error) {
	coefficient, ok := new(big.Int).SetString(value.Coefficient, 10)
	if !ok {
		return nil, fmt.Errorf("inbound: coefficient %q is not an integer", value.Coefficient)
	}
	rat := new(big.Rat).SetInt(coefficient)
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absInt(int(value.Exponent10)))), nil)
	scale := new(big.Rat).SetInt(pow)
	if value.Exponent10 < 0 {
		rat.Quo(rat, scale)
	} else if value.Exponent10 > 0 {
		rat.Mul(rat, scale)
	}
	return rat, nil
}

func canonicalDecimal(value *big.Rat) (semreg.Decimal, error) {
	return canonicalLabeled(value, "temperature")
}

func canonicalLabeled(value *big.Rat, label string) (semreg.Decimal, error) {
	if value.Sign() == 0 {
		return semreg.Decimal{Coefficient: "0", Exponent10: 0}, nil
	}
	numerator := new(big.Int).Set(value.Num())
	denominator := new(big.Int).Set(value.Denom())
	exponent := 0
	two := big.NewInt(2)
	five := big.NewInt(5)
	ten := big.NewInt(10)
	for denominator.Cmp(big.NewInt(1)) != 0 {
		switch {
		case new(big.Int).Mod(denominator, two).Sign() == 0:
			denominator.Quo(denominator, two)
			numerator.Mul(numerator, five)
			exponent--
		case new(big.Int).Mod(denominator, five).Sign() == 0:
			denominator.Quo(denominator, five)
			numerator.Mul(numerator, two)
			exponent--
		default:
			return semreg.Decimal{}, errors.New("inbound: " + label + " is not a finite decimal")
		}
		if exponent < -18 {
			return semreg.Decimal{}, errors.New("inbound: " + label + " exponent is out of range")
		}
	}
	for numerator.Sign() != 0 && new(big.Int).Mod(numerator, ten).Sign() == 0 && exponent < 18 {
		numerator.Quo(numerator, ten)
		exponent++
	}
	if exponent < -18 || exponent > 18 {
		return semreg.Decimal{}, errors.New("inbound: " + label + " exponent is out of range")
	}
	return semreg.Decimal{Coefficient: numerator.String(), Exponent10: int32(exponent)}, nil
}

func convertSymbol(row heatingRow, text string) (semreg.Symbol, bool) {
	if token, ok := row.Symbols[text]; ok {
		return semreg.Symbol{Namespace: semreg.DefinitionID(row.Fact), Token: token, Known: true}, true
	}
	return semreg.Symbol{Namespace: "native.hvac.mode", Token: text, Known: false}, false
}
