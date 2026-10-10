package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/alerts"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// maxLeadDays bounds the renewal reminders' lead days.
const maxLeadDays = 30

// moneyChanged asks the money alerts (subscription detection, large
// debits, low balances, renewals) for a check soon.
func (h *Handler) moneyChanged() {
	if h.onMoneyChange != nil {
		h.onMoneyChange()
	}
}

func (h *Handler) settingsView(settings alerts.Settings) generated.FinanceSettings {
	currency := h.defaultCurrency
	return generated.FinanceSettings{Currency: currency,
		LargeDebit: generated.AmountAlert{Enabled: settings.LargeDebitEnabled, Threshold: money(settings.LargeDebitMinor, currency)},
		LowBalance: generated.AmountAlert{Enabled: settings.LowBalanceEnabled, Threshold: money(settings.LowBalanceMinor, currency)},
		Renewals:   generated.RenewalAlert{Enabled: settings.RenewalsEnabled, LeadDays: settings.LeadDays},
		UpdatedAt:  settings.UpdatedAt}
}

func (h *Handler) GetFinanceSettings(c fiber.Ctx) error {
	settings, err := alerts.LoadSettings(c.Context(), h.queries)
	if err != nil {
		return internal(err)
	}
	return c.JSON(h.settingsView(settings))
}

// requireFields fails unless every name is a property of raw (an object).
func requireFields(raw json.RawMessage, group string, names ...string) error {
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(raw, &properties); err != nil || properties == nil {
		return invalid(group + " must be an object")
	}
	for _, name := range names {
		if _, ok := properties[name]; !ok || isNull(properties, name) {
			return invalid(group + "." + name + " is required")
		}
	}
	return nil
}

func (h *Handler) UpdateFinanceSettings(c fiber.Ctx) error {
	var body generated.UpdateFinanceSettingsRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	for group, names := range map[string][]string{"largeDebit": {"enabled", "thresholdMinor"}, "lowBalance": {"enabled", "thresholdMinor"}, "renewals": {"enabled", "leadDays"}} {
		raw, ok := properties[group]
		if !ok {
			return invalid(group + " is required")
		}
		if err := requireFields(raw, group, names...); err != nil {
			return err
		}
	}
	switch {
	case body.LargeDebit.ThresholdMinor < 1:
		return invalid("largeDebit.thresholdMinor must be at least 1")
	case body.LowBalance.ThresholdMinor < 0:
		return invalid("lowBalance.thresholdMinor must not be negative")
	case body.Renewals.LeadDays < 0 || body.Renewals.LeadDays > maxLeadDays:
		return invalid(fmt.Sprintf("renewals.leadDays must be between 0 and %d", maxLeadDays))
	}
	ctx := c.Context()
	before, err := alerts.LoadSettings(ctx, h.queries)
	if err != nil {
		return internal(err)
	}
	next := alerts.Settings{LargeDebitEnabled: body.LargeDebit.Enabled, LargeDebitMinor: body.LargeDebit.ThresholdMinor,
		LowBalanceEnabled: body.LowBalance.Enabled, LowBalanceMinor: body.LowBalance.ThresholdMinor,
		RenewalsEnabled: body.Renewals.Enabled, LeadDays: body.Renewals.LeadDays}
	changes := settingsChanges(before, next)
	if len(changes) == 0 {
		return c.JSON(h.settingsView(before))
	}
	row, err := h.queries.SaveFinanceSettings(ctx, store.SaveFinanceSettingsParams{LargeDebitEnabled: next.LargeDebitEnabled, LargeDebitMinor: next.LargeDebitMinor,
		LowBalanceEnabled: next.LowBalanceEnabled, LowBalanceMinor: next.LowBalanceMinor, RenewalsEnabled: next.RenewalsEnabled, RenewalLeadDays: int32(next.LeadDays)})
	if err != nil {
		return internal(err)
	}
	saved := alerts.FromRow(row)
	h.recordSettings(ctx, c, saved, changes)
	h.moneyChanged()
	return c.JSON(h.settingsView(saved))
}

func settingsChanges(before, after alerts.Settings) []string {
	var changes []string
	add := func(name string, changed bool) {
		if changed {
			changes = append(changes, name)
		}
	}
	add("largeDebit", before.LargeDebitEnabled != after.LargeDebitEnabled || before.LargeDebitMinor != after.LargeDebitMinor)
	add("lowBalance", before.LowBalanceEnabled != after.LowBalanceEnabled || before.LowBalanceMinor != after.LowBalanceMinor)
	add("renewals", before.RenewalsEnabled != after.RenewalsEnabled || before.LeadDays != after.LeadDays)
	return changes
}

// recordSettings: "Alert settings updated: large debits from €300.00,
// low balance below €100.00 (off), renewal reminders 3 days ahead".
func (h *Handler) recordSettings(ctx context.Context, c fiber.Ctx, settings alerts.Settings, changes []string) {
	format := func(amount int64) string { return financemoney.Format(amount, h.defaultCurrency) }
	off := func(enabled bool) string {
		if enabled {
			return ""
		}
		return " (off)"
	}
	days := fmt.Sprintf("%d days ahead", settings.LeadDays)
	switch settings.LeadDays {
	case 0:
		days = "on the day"
	case 1:
		days = "1 day ahead"
	}
	summary := fmt.Sprintf("Alert settings updated: large debits from %s%s, low balance below %s%s, renewal reminders %s%s",
		format(settings.LargeDebitMinor), off(settings.LargeDebitEnabled), format(settings.LowBalanceMinor), off(settings.LowBalanceEnabled),
		days, off(settings.RenewalsEnabled))
	h.emit(ctx, c, activity.Event{ID: activity.EventID(settingsUpdated, version(settings.UpdatedAt)), Type: settingsUpdated,
		Subject: activity.Subject{Type: "finance_settings", ID: "alerts"}, Summary: summary,
		Data: map[string]any{"changes": changes, "currency": h.defaultCurrency,
			"largeDebitEnabled": settings.LargeDebitEnabled, "largeDebitMinor": settings.LargeDebitMinor,
			"lowBalanceEnabled": settings.LowBalanceEnabled, "lowBalanceMinor": settings.LowBalanceMinor,
			"renewalsEnabled": settings.RenewalsEnabled, "renewalLeadDays": settings.LeadDays}})
}
