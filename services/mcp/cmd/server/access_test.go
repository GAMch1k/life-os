package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/pkg/identity/identitytest"
	"github.com/GAMch1k/life-os/services/mcp/internal/config"
)

// fakeServices answers every domain service's request with an empty
// object and remembers what it was sent.
type fakeServices struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (f *fakeServices) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(context.Background()))
	f.mu.Unlock()
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write([]byte(`{"id":"0f8fad5b-d9cb-469f-a165-70867728950e","items":[]}`))
}

func (f *fakeServices) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

func testConfig(url string) config.Config {
	return config.Config{
		FinanceURL: url, TasksURL: url, InboxURL: url, CalendarURL: url, FoodURL: url,
		RecipeImportURL: url, TimeZone: "UTC",
	}
}

func testBuild(url string) func() *mcp.Server {
	cfg := testConfig(url)
	return func() *mcp.Server {
		return newServer(cfg, &http.Client{Timeout: 5 * time.Second}, activity.NewEmitter("", "mcp", nil), owner{zone: time.UTC, now: time.Now})
	}
}

// Every tool belongs to a module, and every tool the read list names exists:
// a new tool cannot slip past the scopes, nor a renamed one become a write.
func TestEveryToolHasAModule(t *testing.T) {
	names, err := listTools(testBuild("http://127.0.0.1:1")())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 30 {
		t.Fatalf("only %d tools: %v", len(names), names)
	}
	for _, name := range names {
		if _, _, ok := toolAccess(name); !ok {
			t.Errorf("tool %q belongs to no module: add its prefix to toolModules", name)
		}
	}
	for name := range readTools {
		if !slices.Contains(names, name) {
			t.Errorf("readTools names %q, which is no tool", name)
		}
	}
}

type headerTransport struct {
	header http.Header
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for key, values := range h.header {
		r.Header[key] = values
	}
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	header := http.Header{}
	if token != "" {
		header.Set(identity.Header, token)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   url + "/mcp",
		HTTPClient: &http.Client{Transport: headerTransport{header: header}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// An MCP client signed in with OAuth sees and runs only the tools its grant
// allows, and its identity token goes on to the services; the processor
// (no token) keeps every tool.
func TestTheGrantDecidesTheTools(t *testing.T) {
	services := &fakeServices{}
	backend := httptest.NewServer(services)
	t.Cleanup(backend.Close)
	signer := identitytest.New(t)
	tools := newCatalog(testBuild(backend.URL))
	mcpServer := httptest.NewServer(httpHandler(identity.NewVerifier(signer.Keys, nil), tools))
	// Closed after the sessions (cleanups run last first).
	t.Cleanup(mcpServer.Close)

	modules := []string{"tasks", "calendar", "inbox", "food", "finance", "voice", "notifications", "activity"}
	scopes := func(level string) []string {
		var out []string
		for _, module := range modules {
			out = append(out, module+":"+level)
		}
		return out
	}
	reader := signer.TokenWith("", "owner", "mcp", map[string]any{"scp": scopes("read")})
	writer := signer.TokenWith("", "owner", "mcp", map[string]any{"scp": scopes("write")})
	tasksOnly := signer.TokenWith("", "owner", "mcp", map[string]any{"scp": []string{"tasks:read"}})

	internal := toolNames(t, connect(t, mcpServer.URL, ""))
	read := toolNames(t, connect(t, mcpServer.URL, reader))
	write := toolNames(t, connect(t, mcpServer.URL, writer))
	tasks := toolNames(t, connect(t, mcpServer.URL, tasksOnly))
	if len(write) != len(internal) {
		t.Fatalf("mcp:write lists %d tools, the processor %d", len(write), len(internal))
	}
	if len(read) == 0 || len(read) >= len(write) {
		t.Fatalf("mcp:read lists %d of %d", len(read), len(write))
	}
	for _, name := range read {
		if !readTools[name] {
			t.Errorf("a read-only grant lists %q", name)
		}
	}
	for _, name := range tasks {
		if !strings.HasPrefix(name, "task_") || !readTools[name] {
			t.Errorf("tasks:read lists %q", name)
		}
	}
	if !slices.Contains(read, "task_get") || slices.Contains(read, "task_create") {
		t.Fatalf("read: %v", read)
	}

	// A read tool runs, with the person's identity token forwarded.
	session := connect(t, mcpServer.URL, reader)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "task_list", Arguments: map[string]any{"query": "limit=1"},
	})
	if err != nil || result.IsError {
		t.Fatalf("task_list: %v %+v", err, result)
	}
	if got := services.last().Header.Get(identity.Header); got != reader {
		t.Fatalf("the service got identity %q", got)
	}
	// A write tool is refused, listed or not.
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "task_create", Arguments: map[string]any{"title": "Not allowed"},
	})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("task_create with mcp:read: %+v", result)
	}
	before := len(services.requests)
	_, _ = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "finance_create_manual_transaction", Arguments: map[string]any{}})
	if len(services.requests) != before {
		t.Fatal("a refused tool reached a service")
	}

	// The processor's calls carry no identity token, as before.
	internalSession := connect(t, mcpServer.URL, "")
	if result, err := internalSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "task_list", Arguments: map[string]any{"query": "limit=1"},
	}); err != nil || result.IsError {
		t.Fatalf("an internal task_list: %v %+v", err, result)
	}
	if got := services.last().Header.Get(identity.Header); got != "" {
		t.Fatalf("an internal call forwarded identity %q", got)
	}

	// A token that does not verify is refused.
	request, _ := http.NewRequest(http.MethodPost, mcpServer.URL+"/mcp", strings.NewReader(`{}`))
	request.Header.Set(identity.Header, "forged.token.value")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a forged token: %d", response.StatusCode)
	}
}
