package money

import "testing"

func TestParseDecimalReadsBankAndSpreadsheetFormats(t *testing.T) {
	cases := map[string]int64{
		"-12.50": -1250, "-12,50": -1250, "12,5": 1250, "1.234,56": 123456, "1,234.56": 123456,
		"+3": 300, "12,50-": -1250, "€ -4.99": -499, "1,234": 123400, "1.234": 123400,
		"1.234.567": 123456700, "0.01": 1, "-0,5": -50,
	}
	for raw, want := range cases {
		decimal, err := ParseDecimal(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		minor, err := decimal.Minor(2)
		if err != nil || minor != want {
			t.Errorf("%q = %d (%v), want %d", raw, minor, err, want)
		}
	}
	for _, raw := range []string{"", "abc", "12.5.5,1", "--3", "1e5"} {
		if _, err := ParseDecimal(raw); err == nil {
			t.Errorf("%q parsed", raw)
		}
	}
}

func TestDecimalMinorRespectsTheCurrencyExponent(t *testing.T) {
	decimal, _ := ParseDecimal("1,012.345")
	if _, err := decimal.Minor(Exponent("EUR")); err == nil {
		t.Fatal("three decimals accepted for EUR")
	}
	if minor, err := decimal.Minor(Exponent("KWD")); err != nil || minor != 1012345 {
		t.Fatalf("KWD = %d %v", minor, err)
	}
	whole, _ := ParseDecimal("1500")
	if minor, err := whole.Minor(Exponent("JPY")); err != nil || minor != 1500 {
		t.Fatalf("JPY = %d %v", minor, err)
	}
	if got := (Decimal{Mantissa: -1250, Scale: 2}).String(); got != "-12.5" {
		t.Fatalf("canonical = %q", got)
	}
}

func TestFormat(t *testing.T) {
	cases := map[string]string{
		Format(-1250, "EUR"):     "-€12.50",
		Format(123456789, "EUR"): "€1,234,567.89",
		Format(5, "USD"):         "$0.05",
		Format(1200, "CHF"):      "12.00 CHF",
		Format(1500, "JPY"):      "1,500 JPY",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
