package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

// dbSync is a sync of a Deutsche Bank current account whose booked balance
// is the bank's closing balance of an earlier day, with an available balance
// that is only the booked balance of today (no card holds), and a card with
// no reported balance.
func dbSync(to string, booked int64, bookedOn string, transactions []map[string]any) map[string]any {
	giro := map[string]any{"externalId": "acc-1", "name": "Girokonto", "kind": "bank", "currency": "EUR",
		"bookedBalance":    map[string]any{"amount": booked, "currency": "EUR", "date": bookedOn},
		"availableBalance": map[string]any{"amount": 25105, "currency": "EUR", "date": "2026-10-12"}}
	body := bankSync([]map[string]any{giro, cardInput}, transactions)
	body["window"] = map[string]any{"from": "2026-10-01", "to": to}
	return body
}

func (c apiClient) accountByID(id string) generated.Account {
	c.t.Helper()
	return expect[generated.Account](c, http.MethodGet, "/v1/accounts/"+id, nil, http.StatusOK)
}

func TestBankBalanceAddsLaterBookingsAndPendingPayments(t *testing.T) {
	c := newAPIClient(t)
	// Friday's closing balance is 300.67. Bookings through Friday are in it;
	// Monday's -49.62 are not; eleven card holds of -145.35 are pending.
	holds := []int64{-3000, -2500, -2000, -1800, -1500, -1200, -900, -800, -600, -135, -100}
	transactions := []map[string]any{
		bankTx("acc-1", "fri", "booked", "2026-10-09", -1290, "Bäckerei"),
		bankTx("acc-1", "thu", "booked", "2026-10-08", -4500, "Stadtwerke"),
		bankTx("acc-1", "mon-1", "booked", "2026-10-12", -2962, "REWE"),
		bankTx("acc-1", "mon-2", "booked", "2026-10-12", -2000, "DB Vertrieb"),
		bankTx("acc-2", "card-1", "booked", "2026-10-05", -1500, "Shell"),
		bankTx("acc-2", "card-2", "pending", "2026-10-11", -700, "Aral"),
	}
	hold := func(index int) map[string]any {
		return bankTx("acc-1", fmt.Sprintf("hold-%d", index), "pending", "2026-10-11", holds[index], fmt.Sprintf("Shop %d", index))
	}
	for index := range holds {
		transactions = append(transactions, hold(index))
	}
	result := c.syncBank(dbSync("2026-10-12", 30067, "2026-10-09", transactions))
	giroID, cardID := result.AccountIds[0].Id.String(), result.AccountIds[1].Id.String()

	giro := c.accountByID(giroID)
	if giro.BookedBalance.Amount != 25105 || giro.Pending.Amount != -14535 || giro.Balance.Amount != 10570 ||
		giro.BankBalance == nil || giro.BankBalance.Amount != 30067 || giro.BankBalance.Date.String() != "2026-10-09" ||
		giro.Available == nil || giro.Available.Amount != 25105 || giro.BalanceAsOf == nil || giro.TransactionCount != 15 {
		t.Fatalf("giro = %+v", giro)
	}
	// Without a bank balance: the booked transactions, plus pending.
	card := c.accountByID(cardID)
	if card.BookedBalance.Amount != -1500 || card.Pending.Amount != -700 || card.Balance.Amount != -2200 || card.BankBalance != nil {
		t.Fatalf("card = %+v", card)
	}
	// Totals use the same figures: the Cash account has nothing.
	summary := expect[generated.MonthlySummary](c, http.MethodGet, "/v1/summaries/monthly?month=2026-10&currency=EUR", nil, http.StatusOK)
	if summary.TotalBookedBalance.Amount != 25105-1500 || summary.TotalPending.Amount != -14535-700 || summary.TotalBalance.Amount != 10570-2200 {
		t.Fatalf("totals = %+v %+v %+v", summary.TotalBalance, summary.TotalBookedBalance, summary.TotalPending)
	}
	// The low-balance flag follows the balance with pending money: 105.70
	// is below 150.00, though 251.05 booked is not.
	expect[generated.FinanceSettings](c, http.MethodPut, "/v1/settings", map[string]any{
		"largeDebit": map[string]any{"enabled": true, "thresholdMinor": 25000}, "lowBalance": map[string]any{"enabled": true, "thresholdMinor": 15000},
		"renewals": map[string]any{"enabled": true, "leadDays": 3}}, http.StatusOK)
	if giro = c.accountByID(giroID); !giro.LowBalance {
		t.Fatalf("not low at %+v", giro.Balance)
	}

	// Tuesday: the bank still reports Friday's balance. The largest hold is
	// booked under a new id, the next under its own id, and the rest stay
	// pending; the balance does not change, nothing is counted twice.
	next := []map[string]any{
		bankTx("acc-1", "fri", "booked", "2026-10-09", -1290, "Bäckerei"),
		bankTx("acc-1", "mon-1", "booked", "2026-10-12", -2962, "REWE"),
		bankTx("acc-1", "mon-2", "booked", "2026-10-12", -2000, "DB Vertrieb"),
		bankTx("acc-1", "booked-0", "booked", "2026-10-13", -3000, "SHOP  0"),
		bankTx("acc-1", "hold-1", "booked", "2026-10-13", -2500, "Shop 1"),
		transactions[4], transactions[5],
	}
	for index := 2; index < len(holds); index++ {
		next = append(next, hold(index))
	}
	if second := c.syncBank(dbSync("2026-10-13", 30067, "2026-10-09", next)); second.Transactions.RemovedPending != 1 {
		t.Fatalf("second sync = %+v", second)
	}
	giro = c.accountByID(giroID)
	if giro.BookedBalance.Amount != 25105-5500 || giro.Pending.Amount != -14535+5500 || giro.Balance.Amount != 10570 || giro.TransactionCount != 15 {
		t.Fatalf("giro after booking = %+v", giro)
	}
	// A balance dated today has today's bookings in it already.
	c.syncBank(dbSync("2026-10-13", 25105-5500, "2026-10-13", next))
	if giro = c.accountByID(giroID); giro.BookedBalance.Amount != 25105-5500 || giro.Balance.Amount != 10570 {
		t.Fatalf("giro with today's balance = %+v", giro)
	}
	// The newest data is the last sync, on the owner's day.
	if giro.BalanceAsOf.String() != c.handler.ownerDay(*giro.Source.SyncedAt).Format(time.DateOnly) {
		t.Fatalf("balance as of %s, synced at %s", giro.BalanceAsOf, giro.Source.SyncedAt)
	}
}

func TestManualBalanceHasPendingMoney(t *testing.T) {
	c := newAPIClient(t)
	wallet := c.account("Wallet", "cash", "EUR", 10000)
	c.spend(map[string]any{"accountId": wallet.Id, "amount": eur(-1250), "bookingDate": "2026-10-09"})
	c.spend(map[string]any{"accountId": wallet.Id, "amount": eur(-4535), "bookingDate": "2026-10-10", "status": "pending"})
	got := c.accountByID(wallet.Id.String())
	if got.BookedBalance.Amount != 8750 || got.Pending.Amount != -4535 || got.Balance.Amount != 4215 || got.BalanceAsOf != nil || got.BankBalance != nil || got.LowBalance {
		t.Fatalf("wallet = %+v", got)
	}
}
