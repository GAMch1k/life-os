package identity

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestTheMFAAndDevClaims(t *testing.T) {
	key := newKey(t, "auth-a")
	verifier := NewVerifier(keySet(t, key), func() time.Time { return now })
	verify := func(claims map[string]any) (Principal, error) {
		return verifier.Verify(sign(t, key, standardHeader(key.kid), claims))
	}

	session := roleClaims(RoleOwner, ViaSession)
	session["mfa"] = true
	p, err := verify(session)
	if err != nil || !p.MFA {
		t.Fatalf("a session with mfa: %+v %v", p, err)
	}
	p, err = verify(roleClaims(RoleOwner, ViaSession))
	if err != nil || p.MFA {
		t.Fatalf("a session without mfa: %+v %v", p, err)
	}
	// mfa means something on a browser session only.
	for _, via := range []string{ViaDevice, ViaToken, ViaMCP, ViaLegacy} {
		claims := roleClaims(RoleOwner, via)
		claims["mfa"] = true
		p, err = verify(claims)
		if err != nil || p.MFA {
			t.Fatalf("%s claiming mfa: %+v %v", via, p, err)
		}
	}

	token := roleClaims(RoleMember, ViaToken)
	token["dev"] = "claude-code"
	token["scp"] = []string{"tasks:read"}
	p, err = verify(token)
	if err != nil || p.DevTool != "claude-code" || len(p.Scopes) != 1 {
		t.Fatalf("a development tool's token: %+v %v", p, err)
	}
	for _, bad := range []map[string]any{
		{"via": ViaSession, "dev": "claude-code"},
		{"via": ViaToken, "dev": "has space"},
		{"via": ViaToken, "dev": "dev:nested"},
	} {
		claims := roleClaims(RoleMember, bad["via"].(string))
		claims["dev"] = bad["dev"]
		if _, err := verify(claims); err == nil {
			t.Fatalf("refused dev claim %v", bad)
		}
	}
}

func TestScopes(t *testing.T) {
	all := Principal{Kind: KindUser}
	if !all.Allows("tasks", true) || !all.Allows("", true) {
		t.Fatal("no scopes: everything the person may do")
	}
	reader := Principal{Kind: KindUser, Scopes: []string{"tasks:read", "food:write"}}
	for _, tc := range []struct {
		module string
		write  bool
		want   bool
	}{
		{"tasks", false, true},
		{"tasks", true, false},
		{"food", false, true},
		{"food", true, true},
		{"finance", false, false},
		{"", false, false},
	} {
		if got := reader.Allows(tc.module, tc.write); got != tc.want {
			t.Errorf("Allows(%q, %v) = %v", tc.module, tc.write, got)
		}
	}
	none := Principal{Kind: KindUser, Scopes: []string{}}
	if none.Allows("tasks", false) {
		t.Fatal("an empty list of scopes allows nothing")
	}
	for service, module := range map[string]string{
		"tasks": "tasks", "banking": "finance", "google-calendar": "calendar",
		"calendar-feeds": "calendar", "recipe-import": "food", "backup": "", "host-health": "",
	} {
		if got := ServiceModule(service); got != module {
			t.Errorf("ServiceModule(%q) = %q, want %q", service, got, module)
		}
	}
}

func TestRequireScopeMiddleware(t *testing.T) {
	key := newKey(t, "auth-a")
	verifier := NewVerifier(keySet(t, key), func() time.Time { return now })
	app := fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error {
		if public, ok := err.(interface {
			PublicError() (int, string, string)
		}); ok {
			status, code, _ := public.PublicError()
			return c.Status(status).SendString(code)
		}
		return c.SendStatus(500)
	}})
	app.Use(Middleware(verifier, "/health"))
	app.Use(RequireScope("tasks", "/health"))
	app.Get("/health", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/v1/tasks", func(c fiber.Ctx) error { return c.SendString("list") })
	app.Post("/v1/tasks", func(c fiber.Ctx) error { return c.SendString("made") })
	call := func(method, path string, claims map[string]any) int {
		req := httptest.NewRequest(method, path, nil)
		if claims != nil {
			req.Header.Set(Header, sign(t, key, standardHeader(key.kid), claims))
		}
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	scoped := func(scopes ...string) map[string]any {
		claims := roleClaims(RoleMember, ViaToken)
		claims["scp"] = scopes
		return claims
	}
	for _, tc := range []struct {
		name   string
		method string
		path   string
		claims map[string]any
		status int
	}{
		{"an internal caller", http.MethodPost, "/v1/tasks", nil, 200},
		{"a session", http.MethodPost, "/v1/tasks", roleClaims(RoleMember, ViaSession), 200},
		{"a device key", http.MethodPost, "/v1/tasks", roleClaims(RoleMember, ViaDevice), 200},
		{"tasks:read reads", http.MethodGet, "/v1/tasks", scoped("tasks:read"), 200},
		{"tasks:read cannot write", http.MethodPost, "/v1/tasks", scoped("tasks:read"), 403},
		{"tasks:write writes", http.MethodPost, "/v1/tasks", scoped("tasks:write"), 200},
		{"tasks:write reads", http.MethodGet, "/v1/tasks", scoped("tasks:write"), 200},
		{"another module's scope", http.MethodGet, "/v1/tasks", scoped("food:write"), 403},
		{"health stays open", http.MethodGet, "/health", scoped("food:write"), 200},
	} {
		if got := call(tc.method, tc.path, tc.claims); got != tc.status {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.status)
		}
	}
}
