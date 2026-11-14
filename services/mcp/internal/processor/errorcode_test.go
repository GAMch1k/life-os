package processor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/pkg/diagnostics"
)

// What MCP or a tool answered stays in the processor's records; a log line
// has its status and code only (docs/architecture/multi-user.md#logs). The
// messages are unchanged, since the processor matches them.
func TestMCPErrorsAreLoggedAsCodes(t *testing.T) {
	marker := "Budget plan for Anna: 4711.93 EUR"
	rejected := &toolRejection{message: "MCP tool finance_fail_budget_plan rejected: finance service returned 409: plan_not_running " + marker}
	status := &mcpStatusError{status: 502, message: "MCP returned 502 Bad Gateway: " + marker}
	bare := &toolRejection{message: "MCP tool finance_fail_budget_plan rejected"}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{rejected, "http_409_tool_rejected"},
		{fmt.Errorf("call MCP tool x: %w", status), "http_502"},
		{bare, "tool_rejected"},
	} {
		got := diagnostics.ErrorCode(tc.err)
		if got != tc.want || strings.Contains(got, "Anna") {
			t.Errorf("ErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	if !strings.Contains(rejected.Error(), "service returned 409") || !strings.HasPrefix(status.Error(), "MCP returned 502") {
		t.Errorf("the messages the processor matches changed: %q, %q", rejected, status)
	}
}
