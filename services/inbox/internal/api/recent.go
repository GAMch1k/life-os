package api

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/inbox/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

const (
	// recentCaptureMinutes and recentCaptureLimit are GET
	// /v1/recent-captures' defaults: the window before the capture, and how
	// many of the latest captures in it are listed.
	recentCaptureMinutes = 20
	recentCaptureLimit   = 10
	// maxFollowUps bounds the earlier captures one attempt records as
	// followed up.
	maxFollowUps = 10
)

// ListRecentCaptures lists the captures made shortly before one capture,
// with what their processing did: the processor's context for a follow-up
// capture ("actually oat milk" after "buy milk").
func (h *Handler) ListRecentCaptures(c fiber.Ctx, params generated.ListRecentCapturesParams) error {
	minutes, limit := recentCaptureMinutes, recentCaptureLimit
	if params.Minutes != nil {
		minutes = *params.Minutes
	}
	if params.Limit != nil {
		limit = *params.Limit
	}
	if minutes < 1 || minutes > 120 {
		return fiber.NewError(400, "minutes must be between 1 and 120")
	}
	if limit < 1 || limit > 50 {
		return fiber.NewError(400, "limit must be between 1 and 50")
	}
	before, err := h.queries.GetInboxItem(c.Context(), pgUUID(params.Before))
	if err != nil {
		return databaseError(err)
	}
	end := before.CreatedAt.Time
	rows, err := h.queries.ListRecentCaptures(c.Context(), store.ListRecentCapturesParams{
		BeforeID:    before.ID,
		WindowStart: pgtype.Timestamptz{Time: end.Add(-time.Duration(minutes) * time.Minute), Valid: true},
		WindowEnd:   pgtype.Timestamptz{Time: end, Valid: true},
		PageLimit:   int32(limit),
	})
	if err != nil {
		return internal(err)
	}
	items := make([]generated.RecentCapture, 0, len(rows))
	for _, row := range rows {
		items = append(items, recentCaptureOutput(row))
	}
	return c.JSON(generated.RecentCaptureList{Items: items})
}

func recentCaptureOutput(row store.ListRecentCapturesRow) generated.RecentCapture {
	correlation := uuid(row.ID).String()
	if row.CorrelationID != nil && *row.CorrelationID != "" {
		correlation = *row.CorrelationID
	}
	capture := generated.RecentCapture{
		Id:            uuid(row.ID),
		Content:       row.Content,
		Source:        generated.RecentCaptureSource(row.Source),
		State:         generated.RecentCaptureState(row.State),
		CorrelationId: correlation,
		CreatedAt:     row.CreatedAt.Time,
		ResolvedAt:    nullableTime(row.ResolvedAt),
	}
	if row.AttemptID.Valid {
		actions := []map[string]interface{}{}
		_ = json.Unmarshal(row.AttemptActions, &actions)
		if actions == nil {
			actions = []map[string]interface{}{}
		}
		capture.Outcome = &generated.RecentCaptureOutcome{
			AttemptId:    uuid(row.AttemptID),
			Status:       generated.RecentCaptureOutcomeStatus(row.AttemptStatus),
			ErrorSummary: row.AttemptErrorSummary,
			CompletedAt:  nullableTime(row.AttemptCompletedAt),
			Actions:      actions,
		}
	}
	return capture
}

// followUp is an earlier capture whose entities a processed attempt
// changed, with those changes.
type followUp struct {
	itemID  string
	changes []map[string]any
}

// followUps reads, from a processed attempt's actions, the earlier captures
// they name as followsUp.inboxItemId (a capture's own id is ignored), in
// order and at most maxFollowUps.
func followUps(itemID string, actions []map[string]interface{}) []followUp {
	var found []followUp
	index := map[string]int{}
	for _, action := range actions {
		link, _ := action["followsUp"].(map[string]interface{})
		raw, _ := link["inboxItemId"].(string)
		id, err := googleuuid.Parse(raw)
		if err != nil || id.String() == itemID {
			continue
		}
		change := map[string]any{}
		if tool, ok := action["tool"].(string); ok {
			change["tool"] = tool
		}
		for _, field := range []string{"entity", "entityId"} {
			if value, ok := link[field].(string); ok && value != "" {
				change[field] = activity.Preview(value, 64)
			}
		}
		at, seen := index[id.String()]
		if !seen {
			if len(found) == maxFollowUps {
				continue
			}
			at = len(found)
			index[id.String()] = at
			found = append(found, followUp{itemID: id.String()})
		}
		found[at].changes = append(found[at].changes, change)
	}
	return found
}

// recordFollowUps records, for a processed attempt, one inbox.item.followed_up
// event per earlier capture whose entities it changed ("actually oat milk"
// renamed the item "buy milk" added). The event is about the earlier capture
// (its subject), so the Event Log shows it in that capture's history, while
// it belongs to this capture's workflow, caused by its processing.completed.
func (h *Handler) recordFollowUps(c fiber.Ctx, item store.InboxItem, attemptID string, actions []map[string]interface{}) {
	itemID := uuid(item.ID).String()
	completed := activity.EventID("inbox.processing.completed", attemptID)
	for _, link := range followUps(itemID, actions) {
		earlierID, err := googleuuid.Parse(link.itemID)
		if err != nil {
			continue
		}
		earlier, err := h.queries.GetInboxItem(c.Context(), pgUUID(types.UUID(earlierID)))
		if err != nil {
			// Not a capture Inbox has: nothing to link.
			continue
		}
		h.record(c, item, activity.Event{
			ID:          activity.EventID("inbox.item.followed_up", attemptID, link.itemID),
			Type:        "inbox.item.followed_up",
			Subject:     activity.Subject{Type: "inbox_item", ID: link.itemID},
			CausationID: &completed,
			Outcome:     activity.Succeeded,
			Summary:     fmt.Sprintf("“%s” changed what “%s” did", activity.Preview(item.Content, 60), activity.Preview(earlier.Content, 60)),
			Data: map[string]any{
				"inboxItemId":           itemID,
				"attemptId":             attemptID,
				"content":               activity.Preview(item.Content, 280),
				"followedItemId":        link.itemID,
				"followedCorrelationId": correlationID(earlier),
				"followedContent":       activity.Preview(earlier.Content, 280),
				"changes":               link.changes,
			},
		})
	}
}
