package alerts

import (
	"strings"
	"testing"

	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// The low-balance alert looks at the balance with pending money: the bank's
// booked balance, the bookings after its date, and the pending card holds.
// A hold the bank books keeps the balance, so the episode stays.
func TestLowBalanceCountsLaterBookingsAndPendingHolds(t *testing.T) {
	w := newWorld(t)
	w.settings(func(p *store.SaveFinanceSettingsParams) { p.LowBalanceMinor = 15000 })
	girokonto := w.bankAccount("Girokonto", "bank", "EUR", "acc-1", pointerTo(int64(30067)))
	w.sync(girokonto, 30067, "2026-10-09")
	w.pay(girokonto, -1290, "2026-10-09", "booked", "Bäckerei", "") // in Friday's balance
	w.pay(girokonto, -4962, "2026-10-12", "booked", "REWE", "")
	if _, sent := w.run("2026-10-12 09:00"); len(sent) != 0 {
		t.Fatalf("251.05 booked is fine: %s", titles(sent))
	}

	hold := w.pay(girokonto, -9035, "2026-10-11", "pending", "MediaMarkt", "")
	w.pay(girokonto, -5500, "2026-10-12", "pending", "Tankstelle", "")
	result, sent := w.run("2026-10-12 10:00")
	if result.Sent != 1 || len(sent) != 1 || sent[0].Title != "Low balance: Girokonto at €105.70" ||
		sent[0].Body != "Below your €150.00 alert. Booked €251.05, pending -€145.35." || !strings.HasSuffix(sent[0].DedupeKey, ":1") {
		t.Fatalf("low with holds: %+v %+v", result, sent)
	}

	// The bank books the larger hold under a new id: the sync removes the
	// pending one and adds the booking (in one database transaction).
	if _, err := w.pool.Exec(t.Context(), `DELETE FROM transactions WHERE id = $1`, hold.ID); err != nil {
		t.Fatal(err)
	}
	w.pay(girokonto, -9035, "2026-10-13", "booked", "MEDIAMARKT", "")
	if result, sent := w.run("2026-10-13 09:00"); len(sent) != 0 || result.Handled != 1 {
		t.Fatalf("booked hold, same episode: %+v %s", result, titles(sent))
	}
	// Then the bank's balance moves to Tuesday with the booking in it.
	w.sync(girokonto, 30067-4962-9035, "2026-10-13")
	if result, sent := w.run("2026-10-13 10:00"); len(sent) != 0 || result.Handled != 1 {
		t.Fatalf("new bank balance, same episode: %+v %s", result, titles(sent))
	}

	// A hold the shop releases gives the money back and re-arms the alert;
	// a new hold below the threshold is a new episode.
	if _, err := w.pool.Exec(t.Context(), `DELETE FROM transactions WHERE account_id = $1 AND status = 'pending'`, girokonto.ID); err != nil {
		t.Fatal(err)
	}
	if _, sent := w.run("2026-10-13 11:00"); len(sent) != 0 {
		t.Fatalf("released: %s", titles(sent))
	}
	w.pay(girokonto, -2000, "2026-10-13", "pending", "Hotel", "")
	if _, sent := w.run("2026-10-13 12:00"); len(sent) != 1 || !strings.HasSuffix(sent[0].DedupeKey, ":2") ||
		sent[0].Title != "Low balance: Girokonto at €140.70" {
		t.Fatalf("new hold: %+v", sent)
	}
}
