// Package api contains the Notifications service's HTTP transport adapters.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
)

// DB is the database the handler needs; *pgxpool.Pool satisfies it.
type DB interface {
	store.DBTX
	Begin(context.Context) (pgx.Tx, error)
}

type Handler struct {
	db       DB
	channels []string
	wake     func()
	activity *activity.Emitter
	// push is web push when its VAPID keys are configured, else nil.
	push Pusher
	now  func() time.Time
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves notifications from db. channels names the enabled
// delivery channels: each new notification gets one pending delivery per
// channel.
func NewHandler(db DB, channels []string) *Handler {
	if channels == nil {
		channels = []string{}
	}
	return &Handler{db: db, channels: channels, wake: func() {}, now: time.Now}
}

// WithWake sets the function that tells the delivery worker a new
// notification is waiting.
func (h *Handler) WithWake(wake func()) *Handler {
	if wake != nil {
		h.wake = wake
	}
	return h
}

// WithActivity records notification.created events through emitter.
func (h *Handler) WithActivity(emitter *activity.Emitter) *Handler {
	h.activity = emitter
	return h
}

func (h *Handler) CreateNotification(c fiber.Ctx) error {
	var body generated.CreateNotificationRequest
	if err := c.Bind().Body(&body); err != nil {
		return badRequest("the body must be a JSON notification")
	}
	trace := activity.FromRequest(c)
	params, err := newNotification(body, trace.Actor, h.now())
	if err != nil {
		return badRequest(err.Error())
	}
	if trace.CorrelationID != "" {
		params.CorrelationID = &trace.CorrelationID
	}

	ctx := c.Context()
	var row store.Notification
	var deliveries []store.Delivery
	created := false
	err = pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		inserted, err := q.InsertNotification(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			// The dedupe key was used before (also by a concurrent request,
			// whose commit the insert waited for): return the original.
			if row, err = q.GetNotificationByDedupeKey(ctx, params.DedupeKey); err != nil {
				return err
			}
			deliveries, err = q.ListDeliveries(ctx, []pgtype.UUID{row.ID})
			return err
		}
		if err != nil {
			return err
		}
		row, created = inserted, true
		for _, channel := range h.channels {
			delivery, err := q.InsertDelivery(ctx, store.InsertDeliveryParams{NotificationID: row.ID, Channel: channel})
			if err != nil {
				return err
			}
			deliveries = append(deliveries, delivery)
		}
		return nil
	})
	if err != nil {
		return internal(err)
	}
	if !created {
		return c.JSON(output(row, deliveries))
	}
	if len(deliveries) > 0 {
		h.wake()
	}
	h.recordCreated(c, trace, row)
	return c.Status(fiber.StatusCreated).JSON(output(row, deliveries))
}

func (h *Handler) recordCreated(c fiber.Ctx, trace activity.Trace, row store.Notification) {
	id := uuid.UUID(row.ID.Bytes).String()
	data := map[string]any{
		"importance": row.Importance,
		"category":   row.Category,
		"source":     row.Source,
		"channels":   h.channels,
		"title":      row.Title,
	}
	if row.Link != nil {
		data["link"] = *row.Link
	}
	// Never the body: it may hold personal detail the Event Log does not
	// need.
	h.activity.Emit(c.Context(), activity.Event{
		ID:            activity.EventID("notification.created", id),
		Type:          "notification.created",
		Source:        "notifications",
		Subject:       activity.Subject{Type: "notification", ID: id},
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Actor:         trace.Actor,
		Outcome:       activity.Succeeded,
		Summary:       fmt.Sprintf("Notification “%s” (%s)", activity.Preview(row.Title, 120), row.Importance),
		Data:          data,
		OccurredAt:    row.CreatedAt.Time,
	})
}

func (h *Handler) ListNotifications(c fiber.Ctx, params generated.ListNotificationsParams) error {
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 100 {
			return badRequest("limit must be between 1 and 100")
		}
		limit = *params.Limit
	}
	query := store.ListNotificationsParams{Unread: params.Unread, PageLimit: int32(limit + 1)}
	if params.Importance != nil {
		if !params.Importance.Valid() {
			return badRequest("importance must be low, medium, or high")
		}
		importance := string(*params.Importance)
		query.Importance = &importance
	}
	if params.Category != nil {
		if len(*params.Category) > maxCategory || !categoryFilter.MatchString(*params.Category) {
			return badRequest("category must be a dotted lowercase name or prefix, such as calendar or calendar.reminder")
		}
		query.Category = params.Category
	}
	if params.Source != nil {
		if !validSource(*params.Source) {
			return badRequest("source must be a source name such as calendar")
		}
		query.Source = params.Source
	}
	if params.Cursor != nil {
		at, id, err := decodeCursor(*params.Cursor)
		if err != nil {
			return badRequest("invalid cursor")
		}
		query.CursorAt = pgtype.Timestamptz{Time: at, Valid: true}
		query.CursorID = pgtype.UUID{Bytes: id, Valid: true}
	}

	ctx := c.Context()
	q := store.New(h.db)
	rows, err := q.ListNotifications(ctx, query)
	if err != nil {
		return internal(err)
	}
	result := generated.NotificationList{Items: make([]generated.Notification, 0, min(len(rows), limit))}
	if len(rows) > limit {
		last := rows[limit-1]
		cursor := encodeCursor(last.CreatedAt.Time, uuid.UUID(last.ID.Bytes))
		result.NextCursor = &cursor
		rows = rows[:limit]
	}
	byNotification, err := h.deliveries(ctx, q, rows)
	if err != nil {
		return internal(err)
	}
	for _, row := range rows {
		result.Items = append(result.Items, output(row, byNotification[row.ID.Bytes]))
	}
	return c.JSON(result)
}

func (h *Handler) GetNotification(c fiber.Ctx, id types.UUID) error {
	q := store.New(h.db)
	row, err := q.GetNotification(c.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return internal(err)
	}
	return h.respond(c, q, row)
}

func (h *Handler) MarkNotificationRead(c fiber.Ctx, id types.UUID) error {
	q := store.New(h.db)
	row, err := q.MarkNotificationRead(c.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return internal(err)
	}
	return h.respond(c, q, row)
}

func (h *Handler) MarkAllNotificationsRead(c fiber.Ctx) error {
	var body generated.MarkAllReadRequest
	if raw := c.Body(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return badRequest("the body must be empty or a JSON object such as {\"before\": \"2026-10-10T12:00:00Z\"}")
		}
	}
	before := pgtype.Timestamptz{}
	if body.Before != nil {
		before = pgtype.Timestamptz{Time: body.Before.UTC(), Valid: true}
	}
	marked, err := store.New(h.db).MarkAllNotificationsRead(c.Context(), before)
	if err != nil {
		return internal(err)
	}
	return c.JSON(generated.MarkAllReadResult{Marked: int(marked)})
}

func (h *Handler) GetUnreadCount(c fiber.Ctx) error {
	counts, err := store.New(h.db).CountUnreadNotifications(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(generated.UnreadCount{Count: int(counts.Count), HighCount: int(counts.HighCount)})
}

func (h *Handler) respond(c fiber.Ctx, q *store.Queries, row store.Notification) error {
	byNotification, err := h.deliveries(c.Context(), q, []store.Notification{row})
	if err != nil {
		return internal(err)
	}
	return c.JSON(output(row, byNotification[row.ID.Bytes]))
}

// deliveries loads the deliveries of rows in one query.
func (h *Handler) deliveries(ctx context.Context, q *store.Queries, rows []store.Notification) (map[[16]byte][]store.Delivery, error) {
	result := map[[16]byte][]store.Delivery{}
	if len(rows) == 0 {
		return result, nil
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	deliveries, err := q.ListDeliveries(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, delivery := range deliveries {
		result[delivery.NotificationID.Bytes] = append(result[delivery.NotificationID.Bytes], delivery)
	}
	return result, nil
}

func output(row store.Notification, deliveries []store.Delivery) generated.Notification {
	result := generated.Notification{
		Id:         types.UUID(row.ID.Bytes),
		Title:      row.Title,
		Body:       row.Body,
		Importance: generated.NotificationImportance(row.Importance),
		Category:   row.Category,
		Source:     row.Source,
		Link:       row.Link,
		DedupeKey:  row.DedupeKey,
		OccurredAt: row.OccurredAt.Time.UTC(),
		CreatedAt:  row.CreatedAt.Time.UTC(),
		ReadAt:     timestamp(row.ReadAt),
		Deliveries: make([]generated.Delivery, 0, len(deliveries)),
	}
	for _, delivery := range deliveries {
		result.Deliveries = append(result.Deliveries, generated.Delivery{
			Channel:       delivery.Channel,
			Status:        generated.DeliveryStatus(delivery.Status),
			Attempts:      int(delivery.Attempts),
			LastError:     delivery.LastError,
			NextAttemptAt: timestamp(delivery.NextAttemptAt),
			SentAt:        timestamp(delivery.SentAt),
		})
	}
	return result
}

func timestamp(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

func badRequest(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "bad_request", message)
}

func notFound() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "notification not found")
}

func internal(err error) error {
	return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "Internal Server Error")
}
