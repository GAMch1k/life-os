package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// ownerDefaults are the owner's default reminders for the events they create
// in Life OS.
type ownerDefaults struct {
	timed, allDay []generated.Reminder
	updatedAt     *time.Time
}

// ownerDefaults reads the settings; before their first change there are no
// defaults.
func (h *Handler) ownerDefaults(ctx context.Context) (ownerDefaults, error) {
	row, err := h.queries.GetSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ownerDefaults{timed: []generated.Reminder{}, allDay: []generated.Reminder{}}, nil
	}
	if err != nil {
		return ownerDefaults{}, err
	}
	return defaultsFromStore(row)
}

func defaultsFromStore(row store.CalendarSetting) (ownerDefaults, error) {
	timed, err := decodeReminders(row.TimedDefaultReminders)
	if err != nil {
		return ownerDefaults{}, err
	}
	allDay, err := decodeReminders(row.AllDayDefaultReminders)
	if err != nil {
		return ownerDefaults{}, err
	}
	return ownerDefaults{timed: remindersOrNone(timed), allDay: remindersOrNone(allDay), updatedAt: nullableTime(row.UpdatedAt)}, nil
}

func (d ownerDefaults) settings() generated.CalendarSettings {
	return generated.CalendarSettings{
		DefaultReminders: generated.ReminderDefaults{Timed: d.timed, AllDay: d.allDay},
		UpdatedAt:        d.updatedAt,
	}
}

func (h *Handler) GetSettings(c fiber.Ctx) error {
	defaults, err := h.ownerDefaults(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(defaults.settings())
}

// UpdateSettings replaces the default reminder lists the body names.
func (h *Handler) UpdateSettings(c fiber.Ctx) error {
	var body struct {
		DefaultReminders map[string]json.RawMessage `json:"defaultReminders"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || len(body.DefaultReminders) == 0 {
		return badRequest("the update must name defaultReminders.timed or defaultReminders.allDay")
	}
	params := store.UpdateSettingsParams{}
	targets := map[string]*[]byte{"timed": &params.TimedDefaultReminders, "allDay": &params.AllDayDefaultReminders}
	for name, raw := range body.DefaultReminders {
		target, ok := targets[name]
		if !ok {
			return badRequest("defaultReminders takes only timed and allDay")
		}
		var list []generated.Reminder
		if err := json.Unmarshal(raw, &list); err != nil || list == nil {
			return badRequest("defaultReminders." + name + " must be a list of reminders")
		}
		if err := validateReminders(list, "defaultReminders."+name+" takes at most %d reminders"); err != nil {
			return badRequest(err.Error())
		}
		encoded, err := encodeReminders(&list)
		if err != nil {
			return internal(err)
		}
		*target = encoded
	}
	row, err := h.queries.UpdateSettings(c.Context(), params)
	if err != nil {
		return internal(err)
	}
	defaults, err := defaultsFromStore(row)
	if err != nil {
		return internal(err)
	}
	return c.JSON(defaults.settings())
}
