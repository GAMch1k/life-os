package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Runtime states recorded on an Inbox processing attempt.
const (
	RuntimeSpawning  = "spawning"
	RuntimeRunning   = "running"
	RuntimeCompleted = "completed"
	RuntimeFailed    = "failed"
	RuntimeTimedOut  = "timed_out"
	RuntimeCancelled = "cancelled"
)

// RuntimeEvent reports an attempt's runtime state to the processor's status.
type RuntimeEvent struct {
	ItemID, AttemptID, State, SessionID string
}

// attemptRuntime keeps an attempt's durable runtime record current. The
// attempt already exists in Inbox before Codex is spawned; it is marked
// spawning first, running (with the Codex session id) once the process
// starts, and heartbeats until the attempt's outcome is recorded. A processor
// that dies stops heartbeating, so a later one can reconcile the attempt.
type attemptRuntime struct {
	runner            Runner
	itemID, attemptID string

	mu      sync.Mutex
	state   string
	session string
	stop    chan struct{}
	stopped sync.Once
}

// heartbeatInterval is how often an open attempt refreshes its runtime record.
const heartbeatInterval = 30 * time.Second

func (r Runner) trackRuntime(ctx context.Context, itemID, attemptID, state string) *attemptRuntime {
	rt := &attemptRuntime{runner: r, itemID: itemID, attemptID: attemptID, state: state, stop: make(chan struct{})}
	rt.record(ctx)
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-rt.stop:
				return
			case <-ticker.C:
				rt.record(ctx)
			}
		}
	}()
	return rt
}

// Spawned implements RunObserver.
func (rt *attemptRuntime) Spawned() { rt.set(context.Background(), RuntimeRunning, "") }

// Session implements RunObserver.
func (rt *attemptRuntime) Session(id string) { rt.set(context.Background(), "", id) }

// Tokens forwards the run's token usage to the processor's status.
func (rt *attemptRuntime) Tokens(usage TokenUsage) {
	if rt.runner.OnTokens != nil {
		rt.runner.OnTokens(usage)
	}
}

// finish records the planning run's outcome; the heartbeat continues until
// close, while actions run.
func (rt *attemptRuntime) finish(ctx context.Context, state string) { rt.set(ctx, state, "") }

func (rt *attemptRuntime) close() { rt.stopped.Do(func() { close(rt.stop) }) }

func (rt *attemptRuntime) set(ctx context.Context, state, session string) {
	rt.mu.Lock()
	if state != "" {
		rt.state = state
	}
	if session != "" {
		rt.session = session
	}
	rt.mu.Unlock()
	rt.record(ctx)
}

// record writes the runtime record. It is best effort: a missed heartbeat
// only makes the attempt look stale sooner, and a finished attempt rejects
// late heartbeats.
func (rt *attemptRuntime) record(ctx context.Context) {
	rt.mu.Lock()
	state, session := rt.state, rt.session
	rt.mu.Unlock()
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	args := map[string]any{"itemId": rt.itemID, "attemptId": rt.attemptID, "runtimeState": state, "runtimeOwner": rt.runner.owner()}
	if session != "" {
		args["sessionId"] = session
	}
	_, _ = rt.runner.Tools.Call(recordCtx, "inbox_record_attempt_runtime", args)
	if rt.runner.OnRuntime != nil {
		rt.runner.OnRuntime(RuntimeEvent{ItemID: rt.itemID, AttemptID: rt.attemptID, State: state, SessionID: session})
	}
}

func (r Runner) owner() string {
	if r.Owner != "" {
		return r.Owner
	}
	return "codex-inbox-processor"
}

// runtimeOutcome maps a planning error to the attempt's runtime state.
func runtimeOutcome(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return RuntimeCompleted
	case ctx.Err() != nil:
		return RuntimeCancelled
	case errors.Is(err, ErrRunTimedOut):
		return RuntimeTimedOut
	default:
		return RuntimeFailed
	}
}

// Reconcile finds attempts abandoned by a processor that stopped (crash, kill,
// restart without a clean shutdown) and returns their captures to the queue.
// Planning is read-only and every domain action carries a deterministic
// idempotency key, so running the capture again cannot duplicate a mutation.
// It returns how many attempts were recovered.
func (r Runner) Reconcile(ctx context.Context, staleAfter time.Duration) (int, error) {
	raw, err := r.Tools.Call(ctx, "inbox_list_stale_attempts", map[string]any{"staleAfterSeconds": int(staleAfter.Seconds())})
	if err != nil {
		return 0, err
	}
	var stale struct {
		Items []struct {
			ID           string  `json:"id"`
			InboxItemID  string  `json:"inboxItemId"`
			Status       string  `json:"status"`
			RuntimeState *string `json:"runtimeState"`
			SessionID    *string `json:"runtimeSessionId"`
			Owner        *string `json:"runtimeOwner"`
		} `json:"items"`
	}
	if err := json.Unmarshal(toolData(raw), &stale); err != nil {
		return 0, fmt.Errorf("decode stale attempts: %w", err)
	}
	recovered := 0
	for _, attempt := range stale.Items {
		if attempt.Owner != nil && *attempt.Owner == r.owner() {
			// This instance's own attempt only missed heartbeats (for example
			// while Inbox was unreachable); it is still running here.
			continue
		}
		state := "untracked"
		if attempt.RuntimeState != nil {
			state = *attempt.RuntimeState
		}
		reason := fmt.Sprintf("recovered: the processor stopped during this attempt (runtime %s)", state)
		if attempt.SessionID != nil {
			reason += "; Codex session " + *attempt.SessionID + " is not resumed because planning is read-only and is re-run"
		}
		if attempt.Status == "pending" {
			// Inbox only finishes attempts that started processing.
			if err := r.record(ctx, attempt.InboxItemID, attempt.ID, "processing", nil, nil); err != nil {
				return recovered, err
			}
		}
		if err := r.schedule(ctx, attempt.InboxItemID, attempt.ID, nil, reason, time.Now(), reasonRecovered); err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}
