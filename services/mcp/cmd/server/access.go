package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
)

// Who may use which tool (docs/services/mcp.md#signing-in-with-oauth): an
// MCP client signed in with OAuth carries the person's identity token
// (via "mcp"), whose scopes say which modules it may read and change; a
// session, legacy access, and the processor on the private network (no
// token) may use every tool. MCP lists only the tools a request's scopes
// allow, refuses any other call, and forwards the token to the services,
// which check it again.

// toolModules are the modules of the tools, by name prefix.
var toolModules = []struct{ prefix, module string }{
	{"inbox_", "inbox"},
	{"task_", "tasks"},
	{"calendar_", "calendar"},
	{"finance_", "finance"},
	{"food_", "food"},
	{"recipe_", "food"},
}

// readTools change nothing; every other tool needs "<module>:write".
var readTools = map[string]bool{
	"inbox_list":                          true,
	"inbox_recent_captures":               true,
	"inbox_list_processing_attempts":      true,
	"inbox_list_stale_attempts":           true,
	"task_get":                            true,
	"task_list":                           true,
	"task_search":                         true,
	"calendar_get":                        true,
	"calendar_list":                       true,
	"calendar_search":                     true,
	"finance_list_accounts":               true,
	"finance_list_categories":             true,
	"finance_month_summary":               true,
	"finance_find_transactions":           true,
	"finance_search_transactions":         true,
	"finance_get_budget_planning_data":    true,
	"finance_list_ai_category_candidates": true,
	"finance_list_budget_plans":           true,
	"food_get_preferences":                true,
	"food_list_ideas":                     true,
	"food_list_meal_plan":                 true,
	"food_list_pantry_items":              true,
	"food_list_recipes":                   true,
	"food_list_shopping_items":            true,
	"food_list_staples":                   true,
	"food_list_suggestions":               true,
}

// toolAccess is the module a tool belongs to and whether it changes
// anything. ok is false for a tool of no known module, which only a
// principal without scopes may use.
func toolAccess(name string) (module string, write bool, ok bool) {
	for _, entry := range toolModules {
		if strings.HasPrefix(name, entry.prefix) {
			return entry.module, !readTools[name], true
		}
	}
	return "", true, false
}

// allows reports whether a request's principal may use a tool: everyone
// without one (an internal caller), and a principal whose scopes cover the
// tool's module, for reading or changing.
func allows(principal *identity.Principal, tool string) bool {
	if principal == nil || principal.Scopes == nil {
		return true
	}
	module, write, ok := toolAccess(tool)
	return ok && principal.Allows(module, write)
}

const (
	extraPrincipal = "lifeos.principal"
	extraIdentity  = "lifeos.identity"
)

// principalOf is the person a request's token names (TokenInfo, filled by
// httpHandler); nil for an internal caller.
func principalOf(info *auth.TokenInfo) (*identity.Principal, string) {
	if info == nil || info.Extra == nil {
		return nil, ""
	}
	principal, _ := info.Extra[extraPrincipal].(identity.Principal)
	token, _ := info.Extra[extraIdentity].(string)
	if token == "" {
		return nil, ""
	}
	return &principal, token
}

// errToolNotAllowed refuses a call the grant does not cover.
var errToolNotAllowed = errors.New("this tool is not allowed for this connection: connect the app again in Life OS with access to change things, or use a tool that only reads")

// accessMiddleware checks every tool call against the request's scopes and
// carries the person's identity token and principal in ctx: the services
// get the token (coreclient), and MCP's own activity events the person
// (activity.ActorUserID).
func accessMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		var info *auth.TokenInfo
		if extra := req.GetExtra(); extra != nil {
			info = extra.TokenInfo
		}
		principal, token := principalOf(info)
		if principal != nil {
			if method == "tools/call" {
				if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && !allows(principal, params.Name) {
					return nil, errToolNotAllowed
				}
			}
			ctx = identity.WithPrincipal(coreclient.WithIdentity(ctx, token), *principal)
		}
		return next(ctx, method, req)
	}
}

// catalog hands each request the server with the tools its scopes allow,
// built once per set of allowances.
type catalog struct {
	build   func() *mcp.Server
	names   []string
	mu      sync.Mutex
	servers map[string]*mcp.Server
}

func newCatalog(build func() *mcp.Server) *catalog {
	names, err := listTools(build())
	if err != nil {
		// Without the list nothing is left out of the listing; every call
		// is still checked (accessMiddleware), so nothing more is allowed.
		fmt.Fprintln(os.Stderr, "mcp: the tools could not be listed:", err)
	}
	return &catalog{build: build, names: names, servers: map[string]*mcp.Server{}}
}

// listTools are the names of a server's tools.
func listTools(s *mcp.Server) ([]string, error) {
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, err
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "life-os-catalog", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var names []string
	params := &mcp.ListToolsParams{}
	for {
		page, err := session.ListTools(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, tool := range page.Tools {
			names = append(names, tool.Name)
		}
		if page.NextCursor == "" {
			break
		}
		params = &mcp.ListToolsParams{Cursor: page.NextCursor}
	}
	sort.Strings(names)
	return names, nil
}

// server is the MCP server for a principal: every tool for nil (an internal
// caller) or a principal without scopes, else only the allowed ones.
func (c *catalog) server(principal *identity.Principal) *mcp.Server {
	var removed []string
	if principal != nil && principal.Scopes != nil {
		for _, name := range c.names {
			if !allows(principal, name) {
				removed = append(removed, name)
			}
		}
	}
	key := strings.Join(removed, ",")
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.servers[key]; ok {
		return s
	}
	s := c.build()
	s.AddReceivingMiddleware(accessMiddleware)
	if len(removed) > 0 {
		s.RemoveTools(removed...)
	}
	c.servers[key] = s
	return s
}

// httpHandler is MCP's HTTP server: /mcp (the streamable transport) and the
// probes. A request with the gateway's identity token is verified, and its
// principal travels in the go-sdk's TokenInfo, so that a session stays with
// the person who opened it and each tool call is checked against the
// scopes it came with. (MCP's OAuth metadata, which a client reads before it
// signs in, is the Dashboard's, at the site root.)
func httpHandler(verifier *identity.Verifier, tools *catalog) http.Handler {
	mux := http.NewServeMux()
	streamable := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		principal, _ := principalOf(auth.TokenInfoFromContext(r.Context()))
		return tools.server(principal)
	}, nil)
	verify := func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		principal, err := verifier.Verify(token)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		return &auth.TokenInfo{
			UserID:     principal.UserID.String(),
			Scopes:     principal.Scopes,
			Expiration: principal.ExpiresAt,
			Extra:      map[string]any{extraPrincipal: principal, extraIdentity: token},
		}, nil
	}
	withToken := auth.RequireBearerToken(verify, nil)(streamable)
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(identity.Header)
		r = r.Clone(r.Context())
		// The gateway never passes a client's Authorization on; whatever
		// arrives here is replaced by the identity token, or dropped.
		r.Header.Del("Authorization")
		if token == "" {
			streamable.ServeHTTP(w, r)
			return
		}
		r.Header.Set("Authorization", "Bearer "+token)
		withToken.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ready"}`)) })
	return mux
}
