// Package money parses and formats amounts in minor units for Finance.
package money

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// ValidCurrency reports whether code is a three-letter upper-case code.
func ValidCurrency(code string) bool { return currencyCode.MatchString(code) }

var zeroDecimal = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "ISK": true, "JPY": true, "KMF": true, "KRW": true,
	"PYG": true, "RWF": true, "UGX": true, "VND": true, "VUV": true, "XAF": true, "XOF": true, "XPF": true,
}

var threeDecimal = map[string]bool{"BHD": true, "IQD": true, "JOD": true, "KWD": true, "LYD": true, "OMR": true, "TND": true}

// Exponent is the number of minor-unit digits of a currency (2 unless it is
// known to have none or three).
func Exponent(currency string) int {
	switch {
	case zeroDecimal[currency]:
		return 0
	case threeDecimal[currency]:
		return 3
	}
	return 2
}

// Decimal is a parsed decimal amount: Mantissa / 10^Scale.
type Decimal struct {
	Mantissa int64
	Scale    int
}

// String is the canonical form without trailing zeros, such as "-12.5".
func (d Decimal) String() string {
	mantissa, scale := d.Mantissa, d.Scale
	for scale > 0 && mantissa%10 == 0 {
		mantissa /= 10
		scale--
	}
	negative := mantissa < 0
	if negative {
		mantissa = -mantissa
	}
	digits := strconv.FormatInt(mantissa, 10)
	if scale > 0 {
		for len(digits) <= scale {
			digits = "0" + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}

// Minor converts the decimal to minor units of a currency with exponent
// digits. More decimals than the currency has fail unless they are zeros.
func (d Decimal) Minor(exponent int) (int64, error) {
	mantissa, scale := d.Mantissa, d.Scale
	for scale > exponent {
		if mantissa%10 != 0 {
			return 0, fmt.Errorf("has more than %d decimals", exponent)
		}
		mantissa /= 10
		scale--
	}
	for scale < exponent {
		if mantissa > (1<<62)/10 || mantissa < -(1<<62)/10 {
			return 0, errors.New("is too large")
		}
		mantissa *= 10
		scale++
	}
	return mantissa, nil
}

var plainDecimal = regexp.MustCompile(`^(\d+)(?:\.(\d{1,3}))?$`)

// ParseDecimal reads a signed amount as banks and spreadsheets write it:
// "-12.50", "-12,50", "1.234,56", "1,234.56", "+3", "12,50-", or with a
// currency sign. The last '.' or ',' followed by one to three digits is the
// decimal mark when the other mark also occurs or it occurs once and is
// followed by one or two digits; otherwise the marks separate thousands.
func ParseDecimal(raw string) (Decimal, error) {
	value := strings.TrimSpace(raw)
	for _, sign := range []string{"€", "$", "£", " ", " ", "'"} {
		value = strings.ReplaceAll(value, sign, "")
	}
	negative := false
	switch {
	case strings.HasPrefix(value, "-"):
		negative, value = true, value[1:]
	case strings.HasPrefix(value, "+"):
		value = value[1:]
	case strings.HasSuffix(value, "-"):
		negative, value = true, value[:len(value)-1]
	}
	if value == "" {
		return Decimal{}, errors.New("is empty")
	}
	lastDot, lastComma := strings.LastIndex(value, "."), strings.LastIndex(value, ",")
	decimalMark := byte(0)
	switch {
	case lastDot >= 0 && lastComma >= 0:
		decimalMark = '.'
		if lastComma > lastDot {
			decimalMark = ','
		}
	case lastDot >= 0 || lastComma >= 0:
		mark, index := byte('.'), lastDot
		if lastComma >= 0 {
			mark, index = ',', lastComma
		}
		if strings.Count(value, string(mark)) == 1 && len(value)-index-1 <= 2 {
			decimalMark = mark
		}
	}
	integer := value
	if decimalMark != 0 {
		integer = value[:strings.LastIndexByte(value, decimalMark)]
	}
	if groups := strings.FieldsFunc(integer, func(r rune) bool { return r == '.' || r == ',' }); len(groups) > 1 || strings.ContainsAny(integer, ".,") {
		// Thousands separators must group by three.
		if len(groups) == 0 || len(groups[0]) > 3 || strings.Count(integer, ".")+strings.Count(integer, ",") != len(groups)-1 {
			return Decimal{}, errors.New("is not a number")
		}
		for _, group := range groups[1:] {
			if len(group) != 3 {
				return Decimal{}, errors.New("is not a number")
			}
		}
	}
	var normalized strings.Builder
	for index := 0; index < len(value); index++ {
		switch char := value[index]; {
		case char == decimalMark:
			normalized.WriteByte('.')
		case char == '.' || char == ',':
			// A thousands separator.
		default:
			normalized.WriteByte(char)
		}
	}
	parts := plainDecimal.FindStringSubmatch(normalized.String())
	if parts == nil {
		return Decimal{}, errors.New("is not a number")
	}
	digits := parts[1] + parts[2]
	if len(digits) > 17 {
		return Decimal{}, errors.New("is too large")
	}
	mantissa, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Decimal{}, errors.New("is not a number")
	}
	if negative {
		mantissa = -mantissa
	}
	return Decimal{Mantissa: mantissa, Scale: len(parts[2])}, nil
}

var symbols = map[string]string{"EUR": "€", "USD": "$", "GBP": "£"}

// Format writes an amount for people, such as "€1,234.50", "-€12.00", or
// "12.00 CHF".
func Format(minor int64, currency string) string {
	negative := minor < 0
	if negative {
		minor = -minor
	}
	exponent := Exponent(currency)
	digits := strconv.FormatInt(minor, 10)
	for len(digits) <= exponent {
		digits = "0" + digits
	}
	whole, fraction := digits[:len(digits)-exponent], digits[len(digits)-exponent:]
	var grouped strings.Builder
	for index, char := range whole {
		if index > 0 && (len(whole)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(char)
	}
	number := grouped.String()
	if exponent > 0 {
		number += "." + fraction
	}
	sign := ""
	if negative {
		sign = "-"
	}
	if symbol, ok := symbols[currency]; ok {
		return sign + symbol + number
	}
	return sign + number + " " + currency
}
