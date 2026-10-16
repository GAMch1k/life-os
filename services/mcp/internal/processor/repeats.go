package processor

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// Captures sent again (P5-23). The owner sends the same capture twice: a
// share that went out twice, a voice note repeated because nothing seemed
// to happen. A prompt rule about repeats was not followed reliably (Codex
// put a repeated grocery in Human Review as already done), so an exact
// repeat is decided here, before Codex: the same text (trimmed, whitespace
// collapsed, case-folded) and the same images (none, or the same
// references) as a capture of the context window, from the recent captures
// read the capture needs anyway (recent.go). When that capture is resolved
// or waits for Human Review, what it asked for is done or waiting for the
// owner: the repeat is resolved at once, with a processed attempt with no
// actions that says so ("Same as the capture from 17:09, already done") and
// names the capture it repeats (duplicateOf), which Inbox records as
// inbox.item.repeated. When it failed, nothing was done, so the repeat is
// planned as usual. Money is never dropped silently: when that capture
// recorded a transaction (two coffees may well cost the same), the repeat
// goes to Human Review instead, without a Codex run, and the owner edits
// and sends it to record another one. A capture edited and sent again from
// Human Review (sourceMetadata.replacesItemId) is meant as written, so it is
// never a repeat, and the capture it replaces, discarded once the edit is
// in, is left out of Codex's list. A repeat in other words ("lemons please"
// after "buy lemons") is Codex's (recentContextPrompt).

// replacedItem is the capture an item was edited from in Human Review
// (sourceMetadata.replacesItemId), or "" for none.
func replacedItem(item inboxItem) string {
	id, _ := item.SourceMetadata["replacesItemId"].(string)
	return strings.TrimSpace(id)
}

// sameCaptureText reports whether two captures say the same: their texts
// trimmed, with whitespace collapsed, and case-folded.
func sameCaptureText(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " "))
}

// sameMedia reports whether two captures have the same images: none, or
// the same references (content hashes), in any order.
func sameMedia(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// repeatOf is the capture of the window, within minutes before item, that
// item repeats: the latest one with the same text and images that is
// resolved or waits for Human Review. A failed one did nothing, so it is no
// reason to skip the item; nor is one still on its way, which the item is
// planned after (order.go), or a discarded one. An item the owner edited
// and sent again repeats nothing.
func (w recentWindow) repeatOf(item inboxItem, minutes int) (recentCapture, bool) {
	if replacedItem(item) != "" {
		return recentCapture{}, false
	}
	var found recentCapture
	ok := false
	window := time.Duration(minutes) * time.Minute
	for _, capture := range w.Items {
		if capture.ID == item.ID || (capture.State != "resolved" && capture.State != "human_review") {
			continue
		}
		if !item.CreatedAt.IsZero() && (!capture.CreatedAt.Before(item.CreatedAt) || item.CreatedAt.Sub(capture.CreatedAt) > window) {
			continue
		}
		if !sameCaptureText(capture.Content, item.Content) || !sameMedia(capture.MediaReferences, item.MediaReferences) {
			continue
		}
		if !ok || capture.CreatedAt.After(found.CreatedAt) {
			found, ok = capture, true
		}
	}
	return found, ok
}

// repeatOutcome is what becomes of a capture sent again: the summary of
// its attempt, resolved, or, when the capture it repeats recorded money,
// the reason it goes to Human Review (review true). Times are on the
// owner's clock.
func repeatOutcome(earlier recentCapture, location *time.Location) (reason string, review bool) {
	if location == nil {
		location = time.UTC
	}
	at := earlier.CreatedAt.In(location).Format("15:04")
	if money := recordedMoney(earlier, location); len(money) > 0 {
		return fmt.Sprintf("Sent twice: the capture from %s already recorded %s. To record another one, edit and send it.", at, strings.Join(money, " and ")), true
	}
	if earlier.State == "human_review" {
		return fmt.Sprintf("Same as the capture from %s, waiting for your review", at), false
	}
	return fmt.Sprintf("Same as the capture from %s, already done", at), false
}

// recordedMoney is what a capture recorded in Finance: each transaction's
// summary (amount, counterparty, date).
func recordedMoney(capture recentCapture, location *time.Location) []string {
	var money []string
	for _, effect := range recentEffects(capture, location) {
		if effect.Entity != "transaction" {
			continue
		}
		summary := effect.Summary
		if summary == "" {
			summary = "a transaction"
		}
		money = append(money, summary)
	}
	return money
}

// resolveRepeat records the attempt of a capture sent again, processed with
// no actions and the capture it repeats, and resolves the capture, as a
// processed capture is; one repeating recorded money goes to Human Review.
func (r Runner) resolveRepeat(ctx context.Context, trace activity.Trace, item inboxItem, attemptID string, earlier recentCapture) error {
	summary, review := repeatOutcome(earlier, r.location(item))
	r.logf("capture %s repeats capture %s: %s", item.ID, earlier.ID, summary)
	if review {
		return r.humanReview(ctx, item.ID, attemptID, plannedAction{Tool: "none", Arguments: map[string]any{}}, []map[string]any{}, summary)
	}
	if err := r.recordAttempt(ctx, map[string]any{"itemId": item.ID, "attemptId": attemptID, "status": "processed", "actions": []map[string]any{}, "errorSummary": &summary, "duplicateOf": earlier.ID}); err != nil {
		return err
	}
	return r.resolveProcessed(ctx, trace, item.ID, attemptID)
}
