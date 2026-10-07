package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/GAMch1k/life-os/services/mcp/internal/config"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type query struct {
	Query string `json:"query,omitempty" jsonschema:"URL query without leading question mark"`
}
type Output struct {
	Data json.RawMessage `json:"data"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	s := mcp.NewServer(&mcp.Implementation{Name: "life-os", Version: "0.1.0"}, nil)
	add := func(n, d, b, p string) {
		mcp.AddTool(s, &mcp.Tool{Name: n, Description: d}, func(ctx context.Context, _ *mcp.CallToolRequest, in query) (*mcp.CallToolResult, Output, error) {
			body, e := coreclient.Client{BaseURL: b, HTTP: hc}.Do(ctx, http.MethodGet, p+in.Query, nil)
			if e != nil {
				return nil, Output{}, e
			}
			return nil, Output{Data: body}, nil
		})
	}
	add("finance_month_summary", "Get Finance monthly summary", cfg.FinanceURL, "/v1/summaries/monthly?")
	add("finance_find_transactions", "Find Finance transactions", cfg.FinanceURL, "/v1/transactions?")
	add("task_list", "List Tasks", cfg.TasksURL, "/v1/tasks?")
	add("inbox_list", "List Inbox items", cfg.InboxURL, "/v1/inbox-items?")
	add("calendar_list", "List Calendar events", cfg.CalendarURL, "/v1/events?")
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ready"}`)) })
	return http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), mux)
}
