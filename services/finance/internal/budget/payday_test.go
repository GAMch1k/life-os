package budget

import (
	"testing"
	"time"
)

func TestPaydayRecognizesThePayerAboveTheMinimum(t *testing.T) {
	minimum := int64(100000)
	payday := Payday{Enabled: true, Payer: "  acme   robotics ", Minimum: &minimum}
	cases := []struct {
		counterparty string
		amount       int64
		want         bool
	}{
		{"ACME Robotics GmbH", 310000, true},
		{"Lohn ACME  ROBOTICS", 100000, true},
		{"ACME Robotics GmbH", 99999, false},
		{"ACME Robotics GmbH", -310000, false},
		{"ACME Logistics GmbH", 310000, false},
		{"", 310000, false},
	}
	for _, c := range cases {
		if got := payday.Pays(c.counterparty, c.amount); got != c.want {
			t.Errorf("Pays(%q, %d) = %v, want %v", c.counterparty, c.amount, got, c.want)
		}
	}
	payday.Minimum = nil
	if !payday.Pays("ACME Robotics GmbH", 1) {
		t.Error("without a minimum any amount counts")
	}
	payday.Enabled = false
	if payday.Pays("ACME Robotics GmbH", 310000) {
		t.Error("switched off, nothing is the salary")
	}
}

func TestTheSalaryIsForTheNextMonthFromThe15th(t *testing.T) {
	date := func(text string) time.Time {
		parsed, err := time.Parse(time.DateOnly, text)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for booked, want := range map[string]string{"2026-10-28": "2026-11", "2026-10-15": "2026-11", "2026-10-14": "2026-10", "2026-10-03": "2026-10",
		"2026-12-29": "2027-01"} {
		if got := MonthKey(SalaryMonth(date(booked))); got != want {
			t.Errorf("SalaryMonth(%s) = %s, want %s", booked, got, want)
		}
	}
	today := date("2026-10-28")
	if !SalaryRecent(date("2026-09-18"), today) || SalaryRecent(date("2026-09-17"), today) {
		t.Error("a payment is recent for 40 days")
	}
}
