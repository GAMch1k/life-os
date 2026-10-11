package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/GAMch1k/life-os/pkg/activity"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

const (
	idempotencyHeader = "Idempotency-Key"
	idempotencyLimit  = 200
	// keyRetention is how long the response to an Idempotency-Key is kept;
	// expired keys are pruned at most once per pruneEvery.
	keyRetention = 30 * 24 * time.Hour
	pruneEvery   = time.Hour
	// writeAttempts bounds how often a write runs again after a deadlock or
	// a race it resolves by reading again (errRetry).
	writeAttempts = 3
)

// errRetry asks write to run the change again in a new transaction, for a
// race the change lost but can resolve by reading again.
var errRetry = errors.New("food: run the write again")

// result is what a write did: the status and body it answers (no body for
// 204), and the Activity events it records once it is committed.
type result struct {
	status int
	body   any
	events []activity.Event
}

// write runs change in one transaction and answers with its result, then
// records its events. With an Idempotency-Key the response is stored in the
// same transaction: a repeat of the same request (method, path, and body)
// answers it again without running change or recording anything, and the
// same key with another request is refused (409 idempotency_conflict). A
// repeat that arrives while the first is still running waits for it.
func (h *Handler) write(c fiber.Ctx, change func(ctx context.Context, q *store.Queries) (result, error)) error {
	key := c.Get(idempotencyHeader)
	if utf8.RuneCountInString(key) > idempotencyLimit {
		return invalid("Idempotency-Key must be at most 200 characters")
	}
	ctx := c.Context()
	hash := requestHash(c)
	if key != "" {
		h.pruneKeys(ctx)
	}
	for attempt := 1; ; attempt++ {
		var outcome result
		var payload []byte
		var replay *store.IdempotencyKey
		err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
			q := h.queries.WithTx(tx)
			if key != "" {
				_, err := q.ClaimIdempotencyKey(ctx, store.ClaimIdempotencyKeyParams{Key: key, RequestHash: hash})
				if errors.Is(err, pgx.ErrNoRows) {
					stored, err := q.GetIdempotencyKey(ctx, key)
					if err != nil {
						return err
					}
					if stored.RequestHash != hash {
						return apiError(fiber.StatusConflict, "idempotency_conflict", "The Idempotency-Key was already used with a different request")
					}
					replay = &stored
					return nil
				}
				if err != nil {
					return err
				}
			}
			var err error
			if outcome, err = change(ctx, q); err != nil {
				return err
			}
			if outcome.body != nil {
				if payload, err = json.Marshal(outcome.body); err != nil {
					return err
				}
			}
			if key == "" {
				return nil
			}
			// An answer without a body (204) is stored as an empty one.
			return q.CompleteIdempotencyKey(ctx, store.CompleteIdempotencyKeyParams{Key: key, Status: int32(outcome.status), Response: append([]byte{}, payload...)})
		})
		if err != nil && attempt < writeAttempts && retryable(err) {
			continue
		}
		if err != nil {
			return passThrough(err)
		}
		if replay != nil {
			return send(c, int(replay.Status), replay.Response)
		}
		for _, event := range outcome.events {
			h.emit(c, event)
		}
		return send(c, outcome.status, payload)
	}
}

// requestHash identifies a request for its Idempotency-Key: method, path
// with query, and body.
func requestHash(c fiber.Ctx) string {
	sum := sha256.New()
	sum.Write([]byte(c.Method() + " " + c.OriginalURL() + "\n"))
	sum.Write(c.Body())
	return hex.EncodeToString(sum.Sum(nil))
}

// retryable reports a failure that running the write again may not repeat:
// a deadlock, a serialization failure, or a lost race (errRetry).
func retryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40P01" || pgErr.Code == "40001"
	}
	return errors.Is(err, errRetry)
}

func send(c fiber.Ctx, status int, payload []byte) error {
	if len(payload) == 0 {
		return c.SendStatus(status)
	}
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return c.Status(status).Send(payload)
}

// pruneKeys deletes expired Idempotency-Keys, at most once per pruneEvery.
// It is best effort: a failure only leaves them for the next time.
func (h *Handler) pruneKeys(ctx context.Context) {
	now := h.now()
	last := h.lastPrune.Load()
	if now.Unix()-last < int64(pruneEvery/time.Second) || !h.lastPrune.CompareAndSwap(last, now.Unix()) {
		return
	}
	_ = h.queries.PruneIdempotencyKeys(ctx, timestamptz(now.Add(-keyRetention)))
}
