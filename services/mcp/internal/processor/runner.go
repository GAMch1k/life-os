package processor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

type planner interface {
	Plan(context.Context, string) (string, error)
}
type toolCaller interface {
	Call(context.Context, string, map[string]any) (json.RawMessage, error)
}

// Runner turns raw Inbox items into approved Life OS MCP actions. It has no
// database client and resolves an item only after every approved tool succeeds.
type Runner struct {
	Planner planner
	Tools   toolCaller
	Model   string
}
type inboxList struct {
	Items []inboxItem `json:"items"`
}
type inboxItem struct {
	ID             string         `json:"id"`
	Content        string         `json:"content"`
	Source         string         `json:"source"`
	SourceMetadata map[string]any `json:"sourceMetadata"`
}
type plan struct {
	Actions   []plannedAction `json:"actions"`
	Ambiguous bool            `json:"ambiguous"`
	Reason    string          `json:"reason"`
}
type plannedAction struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func (r Runner) Run(ctx context.Context) error {
	raw, err := r.Tools.Call(ctx, "inbox_list", map[string]any{"query": "state=raw&limit=50"})
	if err != nil {
		return err
	}
	var items inboxList
	if err := json.Unmarshal(toolData(raw), &items); err != nil {
		return fmt.Errorf("decode Inbox MCP result: %w", err)
	}
	for _, item := range items.Items {
		if err := r.process(ctx, item); err != nil {
			return err
		}
	}
	return nil
}
func (r Runner) process(ctx context.Context, item inboxItem) error {
	key := idempotencyKey(item)
	attempt, err := r.Tools.Call(ctx, "inbox_start_processing_attempt", map[string]any{"itemId": item.ID, "idempotencyKey": key, "agentName": "codex-inbox-processor", "modelProfile": r.Model})
	if err != nil {
		return fmt.Errorf("start attempt for %s: %w", item.ID, err)
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(toolData(attempt), &started); err != nil || started.ID == "" {
		return fmt.Errorf("decode attempt for %s: %w", item.ID, err)
	}
	if err := r.record(ctx, item.ID, started.ID, "processing", nil, nil); err != nil {
		return err
	}
	prompt, _ := json.Marshal(map[string]any{"content": item.Content, "source": item.Source, "sourceMetadata": item.SourceMetadata})
	rawPlan, err := r.Planner.Plan(ctx, string(prompt))
	if err != nil {
		return r.recover(ctx, item.ID, started.ID, nil, fmt.Sprintf("planning failed: %v", err))
	}
	parsed, err := decodePlan(rawPlan)
	if err != nil || parsed.Ambiguous || len(parsed.Actions) == 0 {
		reason := "no executable action"
		if err != nil {
			reason = "invalid action plan: " + err.Error()
		} else if parsed.Reason != "" {
			reason = parsed.Reason
		}
		return r.recover(ctx, item.ID, started.ID, nil, reason)
	}
	actions := make([]map[string]any, 0, len(parsed.Actions))
	for _, action := range parsed.Actions {
		if !validAction(action) {
			return r.recover(ctx, item.ID, started.ID, actions, "invalid proposed action: "+action.Tool)
		}
		result, err := r.Tools.Call(ctx, action.Tool, action.Arguments)
		audit := map[string]any{"tool": action.Tool, "arguments": action.Arguments}
		if err != nil {
			actions = append(actions, audit)
			return r.recover(ctx, item.ID, started.ID, actions, fmt.Sprintf("%s failed: %v", action.Tool, err))
		}
		audit["result"] = json.RawMessage(toolData(result))
		actions = append(actions, audit)
	}
	if err := r.record(ctx, item.ID, started.ID, "processed", actions, nil); err != nil {
		return err
	}
	_, err = r.Tools.Call(ctx, "inbox_resolve", map[string]any{"itemId": item.ID})
	return err
}
func (r Runner) recover(ctx context.Context, itemID, attemptID string, actions []map[string]any, reason string) error {
	return r.record(ctx, itemID, attemptID, "retryable", actions, &reason)
}
func (r Runner) record(ctx context.Context, itemID, attemptID, status string, actions []map[string]any, summary *string) error {
	_, err := r.Tools.Call(ctx, "inbox_record_processing_attempt", map[string]any{"itemId": itemID, "attemptId": attemptID, "status": status, "actions": actions, "errorSummary": summary})
	return err
}
func allowed(name string) bool { return name == "task_create" || name == "calendar_create" }
func validAction(action plannedAction) bool {
	if !allowed(action.Tool) || action.Arguments == nil {
		return false
	}
	if title, ok := action.Arguments["title"].(string); !ok || strings.TrimSpace(title) == "" {
		return false
	}
	allowedFields := map[string]map[string]bool{
		"task_create":     {"title": true, "notes": true, "dueAt": true},
		"calendar_create": {"title": true, "description": true, "allDay": true, "startAt": true, "endAt": true, "startDate": true, "endDate": true},
	}
	for field := range action.Arguments {
		if !allowedFields[action.Tool][field] {
			return false
		}
	}
	return true
}
func idempotencyKey(item inboxItem) string {
	sum := sha256.Sum256([]byte(item.ID + "\x00" + item.Content))
	return fmt.Sprintf("codex-%x", sum[:12])
}
func decodePlan(raw string) (plan, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var parsed plan
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return plan{}, err
	}
	return parsed, nil
}
func toolData(raw json.RawMessage) json.RawMessage {
	var result struct {
		StructuredContent struct {
			Data json.RawMessage `json:"data"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &result) == nil && len(result.StructuredContent.Data) > 0 {
		return result.StructuredContent.Data
	}
	return raw
}
