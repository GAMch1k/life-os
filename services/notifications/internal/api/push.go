package api

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/delivery"
	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
)

// Pusher is web push as the API uses it; *delivery.WebPushChannel
// satisfies it.
type Pusher interface {
	PublicKey() string
	Test(ctx context.Context, devices []store.PushSubscription, now time.Time) []delivery.PushTestResult
}

// webPushChannel is the channel name that makes notifications go out by web
// push.
const webPushChannel = "webpush"

// maxEndpoint bounds a push service endpoint.
const maxEndpoint = 2048

// pushTestTimeout bounds a test push: the Dashboard waits for it.
const pushTestTimeout = 15 * time.Second

// WithPush enables web push with its VAPID keys: subscriptions and test
// pushes. Without it the push config says not configured and subscribing is
// refused.
func (h *Handler) WithPush(push Pusher) *Handler {
	h.push = push
	return h
}

func (h *Handler) delivering() bool {
	for _, channel := range h.channels {
		if channel == webPushChannel {
			return true
		}
	}
	return false
}

func (h *Handler) GetPushConfig(c fiber.Ctx) error {
	result := generated.PushConfig{Configured: h.push != nil, Delivering: h.push != nil && h.delivering()}
	if h.push != nil {
		key := h.push.PublicKey()
		result.PublicKey = &key
	}
	return c.JSON(result)
}

func (h *Handler) ListPushSubscriptions(c fiber.Ctx) error {
	rows, err := store.New(h.db).ListPushSubscriptions(c.Context())
	if err != nil {
		return internal(err)
	}
	result := generated.PushSubscriptionList{Items: make([]generated.PushSubscription, 0, len(rows))}
	for _, row := range rows {
		result.Items = append(result.Items, subscriptionOutput(row))
	}
	return c.JSON(result)
}

func (h *Handler) CreatePushSubscription(c fiber.Ctx) error {
	if h.push == nil {
		return pushNotConfigured()
	}
	var body generated.PushSubscriptionRequest
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return badRequest("the body must be a browser's push subscription: {\"endpoint\": …, \"keys\": {\"p256dh\": …, \"auth\": …}}")
	}
	params, err := newPushSubscription(body, c.Get(fiber.HeaderUserAgent))
	if err != nil {
		return badRequest(err.Error())
	}
	params.DeviceKeyID = deviceKeyOf(c)
	row, err := store.New(h.db).UpsertPushSubscription(c.Context(), params)
	if err != nil {
		return internal(err)
	}
	status := fiber.StatusOK
	if row.Created {
		status = fiber.StatusCreated
	}
	return c.Status(status).JSON(subscriptionOutput(store.PushSubscription{
		ID: row.ID, Endpoint: row.Endpoint, P256dh: row.P256dh, Auth: row.Auth, DeviceLabel: row.DeviceLabel,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, LastSuccessAt: row.LastSuccessAt,
		LastFailureAt: row.LastFailureAt, LastError: row.LastError, DeviceKeyID: row.DeviceKeyID,
	}))
}

// deviceKeyOf is the paired device's key a request came with (the identity
// token's via "device", its sid): its subscription is deleted when the key
// is revoked (docs/api/devices.md#push-notifications-and-the-key). None for
// a browser's session, legacy access, or an internal caller.
func deviceKeyOf(c fiber.Ctx) pgtype.UUID {
	principal, ok := identity.FromFiber(c)
	if !ok || principal.Via != identity.ViaDevice {
		return pgtype.UUID{}
	}
	id, err := uuid.Parse(principal.CredentialID)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

// DeleteDeviceKeySubscriptions deletes a revoked device key's subscriptions,
// for the Dashboard on the private network. Through the gateway every
// request carries an identity token, so one with a token is refused: no
// person deletes another's devices this way (until P21-07 makes it the
// Dashboard's service assertion).
func (h *Handler) DeleteDeviceKeySubscriptions(c fiber.Ctx, keyID types.UUID) error {
	if _, ok := identity.FromFiber(c); ok {
		return httpserver.NewError(fiber.StatusForbidden, "internal_only",
			"only Life OS itself deletes a device key's subscriptions")
	}
	if _, err := store.New(h.db).DeleteDeviceKeySubscriptions(c.Context(), pgtype.UUID{Bytes: keyID, Valid: true}); err != nil {
		return internal(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) DeletePushSubscription(c fiber.Ctx, id types.UUID) error {
	deleted, err := store.New(h.db).DeletePushSubscription(c.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return internal(err)
	}
	if deleted == 0 {
		return deviceNotFound()
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) SendPushTest(c fiber.Ctx) error {
	if h.push == nil {
		return pushNotConfigured()
	}
	var body generated.PushTestRequest
	if raw := c.Body(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return badRequest("the body must be empty or a JSON object such as {\"subscriptionId\": \"…\"}")
		}
	}
	ctx := c.Context()
	q := store.New(h.db)
	var devices []store.PushSubscription
	if body.SubscriptionId != nil {
		device, err := q.GetPushSubscription(ctx, pgtype.UUID{Bytes: *body.SubscriptionId, Valid: true})
		if errors.Is(err, pgx.ErrNoRows) {
			return deviceNotFound()
		}
		if err != nil {
			return internal(err)
		}
		devices = []store.PushSubscription{device}
	} else {
		var err error
		if devices, err = q.ListPushSubscriptions(ctx); err != nil {
			return internal(err)
		}
	}
	testCtx, cancel := context.WithTimeout(ctx, pushTestTimeout)
	defer cancel()
	result := generated.PushTestResult{Results: make([]generated.PushTestDeviceResult, 0, len(devices))}
	for _, outcome := range h.push.Test(testCtx, devices, h.now()) {
		entry := generated.PushTestDeviceResult{
			SubscriptionId: types.UUID(outcome.Device.ID.Bytes),
			DeviceLabel:    outcome.Device.DeviceLabel,
			Outcome:        generated.PushTestOutcome(outcome.Outcome),
		}
		if outcome.Error != "" {
			text := outcome.Error
			entry.Error = &text
		}
		result.Results = append(result.Results, entry)
	}
	return c.JSON(result)
}

// newPushSubscription validates a browser's subscription and returns the row
// to store, its keys in canonical base64url and its label read from
// userAgent.
func newPushSubscription(body generated.PushSubscriptionRequest, userAgent string) (store.UpsertPushSubscriptionParams, error) {
	endpoint := strings.TrimSpace(body.Endpoint)
	if err := validEndpoint(endpoint); err != nil {
		return store.UpsertPushSubscriptionParams{}, err
	}
	p256dh, err := webpush.DecodeBase64URL(body.Keys.P256dh)
	if err != nil || len(p256dh) != 65 {
		return store.UpsertPushSubscriptionParams{}, errors.New("keys.p256dh must be the browser's P-256 public key: 65 bytes, base64url")
	}
	if _, err := ecdh.P256().NewPublicKey(p256dh); err != nil {
		return store.UpsertPushSubscriptionParams{}, errors.New("keys.p256dh is not a point on P-256")
	}
	auth, err := webpush.DecodeBase64URL(body.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return store.UpsertPushSubscriptionParams{}, errors.New("keys.auth must be the browser's auth secret: 16 bytes, base64url")
	}
	return store.UpsertPushSubscriptionParams{
		Endpoint:    endpoint,
		P256dh:      webpush.EncodeBase64URL(p256dh),
		Auth:        webpush.EncodeBase64URL(auth),
		DeviceLabel: webpush.DeviceLabel(userAgent),
	}, nil
}

// validEndpoint accepts an https URL on a host name, as every push service
// gives: the server posts to it, so an address, a single-label name (a
// container on the private network), credentials, or a fragment are refused.
func validEndpoint(endpoint string) error {
	const message = "endpoint must be the push service's https URL, such as https://fcm.googleapis.com/fcm/send/…"
	if endpoint == "" || len(endpoint) > maxEndpoint {
		return errors.New(message)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New(message)
	}
	host := parsed.Hostname()
	if host == "" || net.ParseIP(host) != nil || !strings.Contains(strings.Trim(host, "."), ".") {
		return errors.New(message)
	}
	return nil
}

func subscriptionOutput(row store.PushSubscription) generated.PushSubscription {
	output := generated.PushSubscription{
		Id:            types.UUID(uuid.UUID(row.ID.Bytes)),
		DeviceLabel:   row.DeviceLabel,
		Endpoint:      row.Endpoint,
		PushService:   webpush.ServiceHost(row.Endpoint),
		CreatedAt:     row.CreatedAt.Time.UTC(),
		LastSuccessAt: timestamp(row.LastSuccessAt),
		LastFailureAt: timestamp(row.LastFailureAt),
		LastError:     row.LastError,
	}
	if row.DeviceKeyID.Valid {
		key := types.UUID(uuid.UUID(row.DeviceKeyID.Bytes))
		output.DeviceKeyId = &key
	}
	return output
}

func pushNotConfigured() error {
	return httpserver.NewError(fiber.StatusConflict, "push_not_configured",
		"web push is not configured on this server: set NOTIFICATIONS_WEBPUSH_VAPID_PUBLIC_KEY and NOTIFICATIONS_WEBPUSH_VAPID_PRIVATE_KEY")
}

func deviceNotFound() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "device not found")
}
