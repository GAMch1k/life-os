package subscriptions

import "testing"

func TestIncomeFindsTheSalaryAndItsPayerText(t *testing.T) {
	payments := []Payment{
		// The salary, a little different each month, at the month's end.
		pay("ACME Robotics GmbH", 310000, "2026-07-30"),
		pay("ACME ROBOTICS GMBH", 312500, "2026-08-28"),
		pay("ACME Robotics GmbH", 309000, "2026-09-29"),
		// A refund from the same company is not part of it.
		pay("ACME Robotics GmbH", 4500, "2026-10-02"),
		// A smaller income that also arrives monthly.
		pay("Kindergeld Familienkasse", 25500, "2026-08-15"),
		pay("Kindergeld Familienkasse", 25500, "2026-09-15"),
		// Big but once, and a known service's refund.
		pay("Erbengemeinschaft Weber", 900000, "2026-09-01"),
		pay("Netflix International B.V.", 1399, "2026-08-10"),
		pay("Netflix International B.V.", 1399, "2026-09-10"),
	}
	found := Income(payments, day("2026-10-12"), euros)
	if len(found) != 2 {
		t.Fatalf("found = %+v", found)
	}
	salary := found[0]
	if salary.Merchant.Key != "cp:acme robotics" || salary.Amount != 310000 || len(salary.Payments) != 3 || !salary.Last.Equal(day("2026-09-29")) {
		t.Fatalf("salary = %+v", salary)
	}
	// The latest counterparty is contained in every one, ignoring case.
	if text := PayerText(salary); text != "ACME Robotics GmbH" {
		t.Fatalf("payer text = %q", text)
	}
	if found[1].Merchant.Key != "cp:kindergeld familienkasse" || len(found[1].Payments) != 2 {
		t.Fatalf("second = %+v", found[1])
	}
}

func TestIncomeNeedsTwoMonthsInARowAndARecentPayment(t *testing.T) {
	cases := map[string][]Payment{
		"one payment":      {pay("ACME Robotics GmbH", 310000, "2026-09-29")},
		"two months apart": {pay("ACME Robotics GmbH", 310000, "2026-07-29"), pay("ACME Robotics GmbH", 310000, "2026-09-29")},
		"twice a month":    {pay("ACME Robotics GmbH", 310000, "2026-09-14"), pay("ACME Robotics GmbH", 310000, "2026-09-29")},
		"stopped":          {pay("ACME Robotics GmbH", 310000, "2026-07-29"), pay("ACME Robotics GmbH", 310000, "2026-08-28")},
		"different sizes":  {pay("ACME Robotics GmbH", 150000, "2026-08-28"), pay("ACME Robotics GmbH", 310000, "2026-09-29")},
	}
	for name, payments := range cases {
		if found := Income(payments, day("2026-10-12"), euros); len(found) != 0 {
			t.Errorf("%s: found %+v", name, found)
		}
	}
}

func TestPayerTextFallsBackToWhatEveryPaymentContains(t *testing.T) {
	candidate := Candidate{Merchant: Merchant{Key: "cp:acme robotics", Name: "ACME Robotics GmbH 4711"}, Payments: []Payment{
		{Counterparty: "ACME Robotics GmbH 4711"}, {Counterparty: "ACME ROBOTICS GMBH 0815"},
	}}
	if text := PayerText(candidate); text != "acme robotics" {
		t.Fatalf("payer text = %q", text)
	}
	candidate.Payments[1].Counterparty = "Lohnbuero"
	if text := PayerText(candidate); text != "" {
		t.Fatalf("payer text = %q", text)
	}
}
