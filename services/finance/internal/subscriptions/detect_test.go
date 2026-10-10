package subscriptions

import (
	"strings"
	"testing"
	"time"
)

func day(text string) time.Time {
	parsed, err := time.Parse(time.DateOnly, text)
	if err != nil {
		panic(err)
	}
	return parsed
}

func euros(string) int { return 2 }

var nextID byte

func pay(counterparty string, amount int64, date string) Payment {
	nextID++
	return Payment{ID: [16]byte{nextID}, AccountID: [16]byte{200}, Amount: amount, Currency: "EUR", Date: day(date), Counterparty: counterparty}
}

func TestMerchantsGroupTheSamePayeeAndLookThroughIntermediaries(t *testing.T) {
	cases := []struct {
		counterparty, description string
		key, name                 string
	}{
		{"NETFLIX.COM", "", "service:netflix", "Netflix"},
		{"Netflix International B.V.", "Netflix Monatsabo 10/2026", "service:netflix", "Netflix"},
		// PayPal and Google Play name the service in the description, as
		// the default Subscriptions rules expect.
		{"PayPal Europe S.a.r.l. et Cie S.C.A", "PP.5323.PP . Spotify AB, Ihr Einkauf bei Spotify AB", "service:spotify", "Spotify"},
		{"Google Payment Ireland Limited", "Google Play YouTube Premium 4711", "service:youtube-premium", "YouTube Premium"},
		{"DisneyPlus", "", "service:disney-plus", "Disney+"},
		{"PayPal Europe S.a.r.l. et Cie S.C.A", "1037594830127 PP.5323.PP . Patreon, Ihr Einkauf bei Patreon", "paypal:patreon", "Patreon via PayPal"},
		{"GOOGLE *Duolingo", "", "google:duolingo", "Duolingo via Google"},
		{"PayPal Europe S.a.r.l. et Cie S.C.A", "", "paypal", "PayPal"},
		{"Stadtwerke Darmstadt GmbH", "Abschlag 4711", "cp:stadtwerke darmstadt", "Stadtwerke Darmstadt GmbH"},
		{"STADTWERKE  DARMSTADT GMBH", "", "cp:stadtwerke darmstadt", "STADTWERKE DARMSTADT GMBH"},
		{"FitX Deutschland GmbH 000123", "", "cp:fitx deutschland", "FitX Deutschland GmbH 000123"},
		{"1&1 Telecom GmbH", "", "cp:telecom", "1&1 Telecom GmbH"},
		{"", "Miete Oktober 2026 Wohnung 3", "text:miete wohnung", "Miete Wohnung"},
		{"", "Miete November 2026 Wohnung 3", "text:miete wohnung", "Miete Wohnung"},
		{"", "SEPA Lastschrift 2026-10", "", ""},
		{"", "", "", ""},
		// Whole words only: "inaudible" is not Audible.
		{"Inaudible Records", "", "cp:inaudible records", "Inaudible Records"},
	}
	for _, c := range cases {
		got := MerchantOf(c.counterparty, c.description)
		if got.Key != c.key || got.Name != c.name {
			t.Errorf("MerchantOf(%q, %q) = %+v, want %s / %s", c.counterparty, c.description, got, c.key, c.name)
		}
	}
}

func candidateText(candidates []Candidate) string {
	var parts []string
	for _, c := range candidates {
		parts = append(parts, c.Merchant.Key+" "+c.Cadence+" "+c.Confidence+" "+c.Last.Format(time.DateOnly)+"→"+c.Next.Format(time.DateOnly))
	}
	return strings.Join(parts, " | ")
}

func TestDetectFindsRegularPaymentsOfSimilarAmounts(t *testing.T) {
	today := day("2026-10-10")
	payments := []Payment{
		// Monthly, with booking dates moving a few days: a candidate.
		pay("NETFLIX.COM", 1399, "2026-07-15"), pay("Netflix.com", 1399, "2026-08-14"), pay("NETFLIX.COM", 1399, "2026-09-16"),
		// Only two monthly payments: not yet.
		pay("Spotify AB", 1099, "2026-08-20"), pay("Spotify AB", 1099, "2026-09-20"),
		// Weekly.
		pay("Wochenmarkt Abo", 2500, "2026-09-12"), pay("Wochenmarkt Abo", 2500, "2026-09-19"), pay("Wochenmarkt Abo", 2600, "2026-09-26"),
		pay("Wochenmarkt Abo", 2500, "2026-10-03"),
		// Quarterly.
		pay("Haftpflicht AG", 4500, "2026-01-05"), pay("Haftpflicht AG", 4500, "2026-04-06"), pay("Haftpflicht AG", 4500, "2026-07-05"),
		// Yearly, seen twice: low confidence.
		pay("ADAC e.V.", 9400, "2025-03-01"), pay("ADAC e.V.", 9400, "2026-03-02"),
		// Yearly but 400 days apart: not regular.
		pay("Domain Registrar", 1500, "2025-01-01"), pay("Domain Registrar", 1500, "2026-02-05"),
		// A café paid at random: never a subscription.
		pay("Café Lotte", 380, "2026-09-01"), pay("Café Lotte", 420, "2026-09-04"), pay("Café Lotte", 390, "2026-09-09"),
		pay("Café Lotte", 410, "2026-09-17"), pay("Café Lotte", 400, "2026-09-19"), pay("Café Lotte", 380, "2026-09-26"),
		// Monthly, but stopped in June: its next payment is long overdue.
		pay("Old Gym", 2999, "2026-04-15"), pay("Old Gym", 2999, "2026-05-15"), pay("Old Gym", 2999, "2026-06-15"),
		// Monthly with a price far outside the tolerance in between.
		pay("Mobilfunk", 1999, "2026-07-01"), pay("Mobilfunk", 4999, "2026-08-01"), pay("Mobilfunk", 1999, "2026-09-01"),
	}
	got := candidateText(Detect(payments, today, euros))
	want := strings.Join([]string{
		"cp:adac yearly low 2026-03-02→2027-03-02",
		"cp:haftpflicht quarterly high 2026-07-05→2026-10-05",
		"cp:wochenmarkt abo weekly high 2026-10-03→2026-10-10",
		"service:netflix monthly high 2026-09-16→2026-10-16",
	}, " | ")
	if got != want {
		t.Fatalf("Detect =\n%s\nwant\n%s", got, want)
	}
}

func TestDetectUsesTheLatestRunAndTypicalAmount(t *testing.T) {
	today := day("2026-10-10")
	payments := []Payment{
		// An old irregular payment, then a regular run with a price rise
		// inside the tolerance: the median of the last three.
		pay("Streaming Plus", 999, "2026-03-03"),
		pay("Streaming Plus", 1299, "2026-07-01"), pay("Streaming Plus", 1299, "2026-08-01"), pay("Streaming Plus", 1399, "2026-09-01"),
		pay("Streaming Plus", 1399, "2026-10-01"),
		// One merchant, two cadences at different amounts: both.
		pay("Sportverein", 500, "2026-09-12"), pay("Sportverein", 500, "2026-09-19"), pay("Sportverein", 500, "2026-09-26"),
		pay("Sportverein", 3000, "2026-07-30"), pay("Sportverein", 3000, "2026-08-30"), pay("Sportverein", 3000, "2026-09-30"),
		// Month ends: the 31st moves to the month's last day.
		pay("Miete GmbH", 90000, "2026-07-31"), pay("Miete GmbH", 90000, "2026-08-31"), pay("Miete GmbH", 90000, "2026-09-30"),
	}
	candidates := Detect(payments, today, euros)
	if got := candidateText(candidates); got != "cp:miete monthly high 2026-09-30→2026-10-30 | cp:sportverein monthly high 2026-09-30→2026-10-30 | "+
		"cp:sportverein weekly high 2026-09-26→2026-10-03 | cp:streaming plus monthly high 2026-10-01→2026-11-01" {
		t.Fatalf("Detect = %s", got)
	}
	streaming := candidates[3]
	if streaming.Amount != 1399 || len(streaming.Payments) != 4 || streaming.Payments[0].Date != day("2026-10-01") {
		t.Fatalf("streaming = %d, %d payments, latest %s", streaming.Amount, len(streaming.Payments), streaming.Payments[0].Date)
	}
	if candidates[2].Amount != 500 || candidates[1].Amount != 3000 {
		t.Fatalf("sportverein amounts = %d / %d", candidates[2].Amount, candidates[1].Amount)
	}
}

func TestAmountsAreSimilarWithinTenPercentOrTwoUnits(t *testing.T) {
	cases := []struct {
		amount, reference int64
		exponent          int
		want              bool
	}{
		{1599, 1399, 2, true},     // +€2.00
		{1600, 1399, 2, false},    // +€2.01
		{109000, 100000, 2, true}, // +9% of €1,000
		{111000, 100000, 2, false},
		{12, 10, 0, true}, // two yen
		{13, 10, 0, false},
	}
	for _, c := range cases {
		if got := Similar(c.amount, c.reference, c.exponent); got != c.want {
			t.Errorf("Similar(%d, %d, %d) = %v", c.amount, c.reference, c.exponent, got)
		}
	}
}

func TestNextDatesMonthlyEquivalentsAndRenewals(t *testing.T) {
	for _, c := range []struct{ last, cadence, want string }{
		{"2026-01-31", Monthly, "2026-02-28"},
		{"2026-10-03", Weekly, "2026-10-10"},
		{"2026-11-30", Quarterly, "2027-02-28"},
		{"2028-02-29", Yearly, "2029-02-28"},
	} {
		if got := Next(day(c.last), c.cadence).Format(time.DateOnly); got != c.want {
			t.Errorf("Next(%s, %s) = %s, want %s", c.last, c.cadence, got, c.want)
		}
	}
	for _, c := range []struct {
		amount  int64
		cadence string
		want    int64
	}{{1000, Weekly, 4333}, {1399, Monthly, 1399}, {3000, Quarterly, 1000}, {11988, Yearly, 999}, {9400, Yearly, 783}} {
		if got := MonthlyEquivalent(c.amount, c.cadence); got != c.want {
			t.Errorf("MonthlyEquivalent(%d, %s) = %d, want %d", c.amount, c.cadence, got, c.want)
		}
	}
	next := day("2026-10-13")
	for _, c := range []struct {
		today string
		lead  int
		want  string
	}{
		{"2026-10-09", 3, RenewalScheduled},
		{"2026-10-10", 3, RenewalUpcoming},
		{"2026-10-10", 2, RenewalScheduled},
		{"2026-10-13", 0, RenewalUpcoming},
		{"2026-10-14", 3, RenewalOverdue},
		{"2026-10-20", 3, RenewalOverdue},
		{"2026-10-21", 3, RenewalMissed},
	} {
		if got := Renewal(next, day(c.today), c.lead); got != c.want {
			t.Errorf("Renewal(13 Oct, %s, %d) = %s, want %s", c.today, c.lead, got, c.want)
		}
	}
}

func TestTrackFollowsAMerchantsSimilarPayments(t *testing.T) {
	payments := []Payment{
		pay("NETFLIX.COM", 1399, "2026-08-14"), pay("Netflix International B.V.", 1499, "2026-09-14"),
		pay("NETFLIX.COM", 2999, "2026-09-20"), // another plan: too far off
		pay("Spotify", 1399, "2026-09-15"),
	}
	matched := Track("service:netflix", "EUR", 1399, payments, euros)
	if len(matched) != 2 || matched[0].Date != day("2026-09-14") || matched[1].Date != day("2026-08-14") {
		t.Fatalf("Track = %+v", matched)
	}
}
