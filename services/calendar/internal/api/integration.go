package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// The integration API (P11-06). Integrations are separate services (such
// as google-calendar and calendar-feeds) that give Calendar the events of
// their sources and follow the events changed in Life OS, only through this
// API; Calendar names no provider. See
// docs/services/calendar-integration-api.md.

// Limits of a registration (mirrored in the OpenAPI contract).
const (
	maxExternalID      = 1024
	maxIntegrationName = 500
	maxProviderLabel   = 50
)

// integrationName is an integration's name, the name of its system actor.
var integrationName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func forbidden(message string) error {
	return httpserver.NewError(fiber.StatusForbidden, "forbidden", message)
}

// authorizeIntegration admits only the integration itself
// (system:<integration>) and development tools (dev:<name>).
func authorizeIntegration(c fiber.Ctx, integration string) error {
	if !integrationName.MatchString(integration) {
		return badRequest("the integration name must be lower-case letters, digits, and dashes, such as google-calendar")
	}
	actor, ok := activity.ParseActor(c.Get(activity.ActorHeader))
	if ok && (actor.Kind == "dev" || actor.Kind == "system" && actor.Name == integration) {
		return nil
	}
	return forbidden(fmt.Sprintf("only the %s integration (X-Life-OS-Actor system:%s) or a dev:<name> tool may use its sources", integration, integration))
}

// validExternalID checks a source's or an event's id in its integration.
func validExternalID(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func checkExternalID(externalID string) error {
	if !validExternalID(externalID, maxExternalID) {
		return badRequest(fmt.Sprintf("externalId must be 1 to %d characters without control characters", maxExternalID))
	}
	return nil
}

// requestActor is the request's X-Life-OS-Actor ("user", "agent:<name>",
// "system:<name>", "dev:<name>"), "" when it has none.
func requestActor(c fiber.Ctx) string {
	actor, ok := activity.ParseActor(c.Get(activity.ActorHeader))
	if !ok {
		return ""
	}
	return actorValue(actor)
}

func actorValue(actor activity.Actor) string {
	if actor.Kind == "user" {
		return "user"
	}
	return actor.Kind + ":" + actor.Name
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// changeableHere reports whether row can be changed through this API
// beyond its reminders and priority: a Life OS event, or the event of a
// source that accepts changes (and is not read-only on its own), whose
// change the integration then writes to the provider through the change
// feed.
func changeableHere(ctx context.Context, queries *store.Queries, row store.Event) (bool, error) {
	switch {
	case row.SourceKind == string(generated.EventSourceKindManual):
		return true, nil
	case row.SourceKind != string(generated.EventSourceKindExternal) || !row.SourceID.Valid || row.ReadOnlyReason != nil:
		return false, nil
	}
	source, err := queries.GetSource(ctx, row.SourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !source.ReadOnly, nil
}

// readOnlyHere refuses a change to an imported event that allows only its
// reminders and priority.
func readOnlyHere(row store.Event) error {
	if row.ReadOnlyReason != nil {
		return httpserver.NewError(fiber.StatusConflict, "managed_by_source", *row.ReadOnlyReason+" Its reminders and priority can still be changed here.")
	}
	return managedBySource()
}

func integrationSourceNotFound() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "the integration has no source with this externalId")
}

// integrationSource renders a source for its integration.
func integrationSource(row store.CalendarSource) (generated.IntegrationSource, error) {
	reminders, err := decodeReminders(row.DefaultReminders)
	if err != nil {
		return generated.IntegrationSource{}, err
	}
	return generated.IntegrationSource{
		Id: uuid(row.ID), Integration: row.Integration, ExternalId: row.ExternalID, Provider: row.Provider,
		Name: row.Name, Enabled: row.Enabled, ReadOnly: row.ReadOnly, PrivateLocation: row.PrivateLocation,
		DefaultReminders: reminders, DefaultPriority: generated.EventPriority(row.DefaultPriority),
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}, nil
}

func respondIntegrationSource(c fiber.Ctx, status int, row store.CalendarSource) error {
	item, err := integrationSource(row)
	if err != nil {
		return internal(err)
	}
	return c.Status(status).JSON(item)
}

func (h *Handler) ListIntegrationSources(c fiber.Ctx, integration string) error {
	if err := authorizeIntegration(c, integration); err != nil {
		return err
	}
	rows, err := h.queries.ListIntegrationSources(c.Context(), integration)
	if err != nil {
		return internal(err)
	}
	result := generated.IntegrationSourceList{Items: make([]generated.IntegrationSource, 0, len(rows))}
	for _, row := range rows {
		item, err := integrationSource(row)
		if err != nil {
			return internal(err)
		}
		result.Items = append(result.Items, item)
	}
	return c.JSON(result)
}

func (h *Handler) GetIntegrationSource(c fiber.Ctx, integration, externalID string) error {
	if err := authorizeIntegration(c, integration); err != nil {
		return err
	}
	if err := checkExternalID(externalID); err != nil {
		return err
	}
	row, err := h.queries.GetIntegrationSource(c.Context(), store.GetIntegrationSourceParams{Integration: integration, ExternalID: externalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return integrationSourceNotFound()
	}
	if err != nil {
		return internal(err)
	}
	return respondIntegrationSource(c, fiber.StatusOK, row)
}

func (h *Handler) DeleteIntegrationSource(c fiber.Ctx, integration, externalID string) error {
	if err := authorizeIntegration(c, integration); err != nil {
		return err
	}
	if err := checkExternalID(externalID); err != nil {
		return err
	}
	err := h.inTx(c, func(queries *store.Queries) error {
		row, err := queries.LockIntegrationSource(c.Context(), store.LockIntegrationSourceParams{Integration: integration, ExternalID: externalID})
		if errors.Is(err, pgx.ErrNoRows) {
			return integrationSourceNotFound()
		}
		if err != nil {
			return internal(err)
		}
		if _, err := queries.DeleteIntegrationSource(c.Context(), row.ID); err != nil {
			return internal(err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// registration is a decoded RegisterIntegrationSourceRequest. The owner's
// settings an integration also sets (name, default reminders, default
// priority) are kept as JSON, the form integration_config records them in;
// a nil one says nothing, as a nil privateLocation does.
type registration struct {
	name            string
	provider        string
	readOnly        bool
	privateLocation *bool
	reminders       []byte // JSON: null for none
	priority        []byte // JSON string
}

func invalidSource(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "invalid_source", message)
}

func decodeRegistration(raw []byte) (registration, error) {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil || present == nil {
		return registration{}, invalidSource("the registration must be a JSON object")
	}
	var body struct {
		Name             *string                  `json:"name"`
		Provider         *string                  `json:"provider"`
		ReadOnly         *bool                    `json:"readOnly"`
		PrivateLocation  *bool                    `json:"privateLocation"`
		DefaultReminders *[]generated.Reminder    `json:"defaultReminders"`
		DefaultPriority  *generated.EventPriority `json:"defaultPriority"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return registration{}, invalidSource("invalid registration: " + err.Error())
	}
	var out registration
	if body.Name == nil {
		return registration{}, invalidSource("name is required")
	}
	out.name = strings.TrimSpace(*body.Name)
	if out.name == "" || utf8.RuneCountInString(out.name) > maxIntegrationName {
		return registration{}, invalidSource(fmt.Sprintf("name must be 1 to %d characters", maxIntegrationName))
	}
	if body.Provider == nil {
		return registration{}, invalidSource("provider is required")
	}
	out.provider = strings.TrimSpace(*body.Provider)
	if out.provider == "" || utf8.RuneCountInString(out.provider) > maxProviderLabel {
		return registration{}, invalidSource(fmt.Sprintf("provider must be 1 to %d characters", maxProviderLabel))
	}
	if body.ReadOnly == nil {
		return registration{}, invalidSource("readOnly is required")
	}
	out.readOnly = *body.ReadOnly
	if _, ok := present["privateLocation"]; ok {
		if body.PrivateLocation == nil {
			return registration{}, invalidSource("privateLocation cannot be null (leave it out to keep the source's)")
		}
		out.privateLocation = body.PrivateLocation
	}
	if _, ok := present["defaultReminders"]; ok {
		if body.DefaultReminders != nil {
			if err := validateReminders(*body.DefaultReminders, "a source takes at most %d default reminders"); err != nil {
				return registration{}, invalidSource(err.Error())
			}
		}
		encoded, err := encodeReminders(body.DefaultReminders)
		if err != nil {
			return registration{}, err
		}
		if encoded == nil {
			encoded = []byte("null")
		}
		out.reminders = encoded
	}
	if value, ok := present["defaultPriority"]; ok {
		if body.DefaultPriority == nil || !body.DefaultPriority.Valid() {
			return registration{}, invalidSource("defaultPriority must be low, medium, or high")
		}
		out.priority = value
	}
	return out, nil
}

// Keys of integration_config.
const (
	configName      = "name"
	configReminders = "defaultReminders"
	configPriority  = "defaultPriority"
)

// sourceConfigNow is what a source's owner settings are now: the last
// applied configuration of a source that has no record of one.
func sourceConfigNow(row store.CalendarSource) map[string]json.RawMessage {
	name, _ := json.Marshal(row.Name)
	reminders := json.RawMessage("null")
	if row.DefaultReminders != nil {
		reminders = row.DefaultReminders
	}
	priority, _ := json.Marshal(row.DefaultPriority)
	return map[string]json.RawMessage{configName: name, configReminders: reminders, configPriority: priority}
}

// RegisterIntegrationSource adds an integration's source or updates it
// (idempotent by integration and externalId). Each owner setting changes
// only when the integration sends another value than it last did
// (integration_config records what it last sent), so the owner's own edits
// stay.
func (h *Handler) RegisterIntegrationSource(c fiber.Ctx, integration, externalID string) error {
	if err := authorizeIntegration(c, integration); err != nil {
		return err
	}
	if err := checkExternalID(externalID); err != nil {
		return err
	}
	reg, err := decodeRegistration(c.Body())
	if err != nil {
		return err
	}
	name, err := json.Marshal(reg.name)
	if err != nil {
		return internal(err)
	}
	sent := map[string]json.RawMessage{configName: name}
	if reg.reminders != nil {
		sent[configReminders] = reg.reminders
	}
	if reg.priority != nil {
		sent[configPriority] = reg.priority
	}
	var row store.CalendarSource
	created := false
	err = h.inTx(c, func(queries *store.Queries) error {
		config, err := json.Marshal(sent)
		if err != nil {
			return internal(err)
		}
		priority := string(defaultPriority)
		if reg.priority != nil {
			_ = json.Unmarshal(reg.priority, &priority)
		}
		row, err = queries.CreateIntegrationSource(c.Context(), store.CreateIntegrationSourceParams{
			Name: reg.name, DefaultReminders: nullJSON(reg.reminders), DefaultPriority: priority,
			Integration: integration, ExternalID: externalID, Provider: reg.provider, ReadOnly: reg.readOnly,
			PrivateLocation: reg.privateLocation != nil && *reg.privateLocation, IntegrationConfig: config,
		})
		if err == nil {
			created = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return internal(err)
		}
		current, err := queries.LockIntegrationSource(c.Context(), store.LockIntegrationSourceParams{Integration: integration, ExternalID: externalID})
		if err != nil {
			return internal(err)
		}
		last := map[string]json.RawMessage{}
		if current.IntegrationConfig == nil || json.Unmarshal(current.IntegrationConfig, &last) != nil {
			last = sourceConfigNow(current)
		}
		params := store.UpdateIntegrationSourceParams{
			ID: current.ID, Name: current.Name, Provider: reg.provider, ReadOnly: reg.readOnly, PrivateLocation: current.PrivateLocation,
			DefaultReminders: current.DefaultReminders, DefaultPriority: current.DefaultPriority,
		}
		if reg.privateLocation != nil {
			params.PrivateLocation = *reg.privateLocation
		}
		for key, value := range sent {
			if previous, ok := last[key]; ok && sameJSON(previous, value) {
				continue // the integration says what it said before: the owner's setting stays
			}
			last[key] = value
			switch key {
			case configName:
				params.Name = reg.name
			case configReminders:
				params.DefaultReminders = nullJSON(value)
			case configPriority:
				_ = json.Unmarshal(value, &params.DefaultPriority)
			}
		}
		if params.IntegrationConfig, err = json.Marshal(last); err != nil {
			return internal(err)
		}
		if params.Name == current.Name && current.Provider == params.Provider && current.ReadOnly == params.ReadOnly &&
			current.PrivateLocation == params.PrivateLocation &&
			sameJSON(current.DefaultReminders, params.DefaultReminders) && current.DefaultPriority == params.DefaultPriority &&
			sameJSON(current.IntegrationConfig, params.IntegrationConfig) {
			row = current
			return nil
		}
		row, err = queries.UpdateIntegrationSource(c.Context(), params)
		if err != nil {
			return internal(err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	status := fiber.StatusOK
	if created {
		status = fiber.StatusCreated
	}
	return respondIntegrationSource(c, status, row)
}

// sameJSON compares two JSON documents by value (JSONB reorders keys);
// absent and null are the same.
func sameJSON(a, b []byte) bool {
	var left, right any
	if len(bytes.TrimSpace(a)) > 0 && json.Unmarshal(a, &left) != nil {
		return false
	}
	if len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, &right) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

// nullJSON is raw, or SQL NULL for absent or JSON null.
func nullJSON(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}
