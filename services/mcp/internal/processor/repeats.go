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
// planned as usual. A repeat in other words ("lemons please" after "buy
// lemons") is Codex's (recentContextPrompt).

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
// planned after (order.go).
func (w recentWindow) repeatOf(item inboxItem, minutes int) (recentCapture, bool) {
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

// repeatSummary is what the attempt of a capture sent again says: when the
// capture it repeats was made, on the owner's clock, and where that one is.
func repeatSummary(earlier recentCapture, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	at := earlier.CreatedAt.In(location).Format("15:04")
	if earlier.State == "human_review" {
		return fmt.Sprintf("Same as the capture from %s, waiting for your review", at)
	}
	return fmt.Sprintf("Same as the capture from %s, already done", at)
}

// resolveRepeat records the attempt of a capture sent again, processed with
// no actions and the capture it repeats, and resolves the capture, as a
// processed capture is.
func (r Runner) resolveRepeat(ctx context.Context, trace activity.Trace, item inboxItem, attemptID string, earlier recentCapture) error {
	summary := repeatSummary(earlier, r.location(item))
	r.logf("capture %s repeats capture %s: %s", item.ID, earlier.ID, summary)
	if err := r.recordAttempt(ctx, map[string]any{"itemId": item.ID, "attemptId": attemptID, "status": "processed", "actions": []map[string]any{}, "errorSummary": &summary, "duplicateOf": earlier.ID}); err != nil {
		return err
	}
	return r.resolveProcessed(ctx, trace, item.ID, attemptID)
}
