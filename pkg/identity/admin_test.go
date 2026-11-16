package identity

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func roleClaims(role, via string) map[string]any {
	claims := standardClaims()
	claims["role"], claims["via"] = role, via
	if via == ViaLegacy {
		claims["sid"] = "legacy"
	}
	return claims
}

// twoFactorClaims is a browser session that signed in with a second factor.
func twoFactorClaims(role, via string) map[string]any {
	claims := roleClaims(role, via)
	claims["mfa"] = true
	return claims
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("not an error envelope: %s", body)
	}
	return envelope.Error.Code
}

func TestCheckAdmin(t *testing.T) {
	user := func(role, via string) Principal { return Principal{Kind: KindUser, Role: role, Via: via} }
	twoFactor := func(role string) Principal {
		return Principal{Kind: KindUser, Role: role, Via: ViaSession, MFA: true}
	}
	// What a forged principal could claim: mfa on a device key.
	keyWithMFA := Principal{Kind: KindUser, Role: RoleOwner, Via: ViaDevice, MFA: true}
	cases := []struct {
		name      string
		principal Principal
		ok        bool
		policy    AdminPolicy
		want      string
	}{
		{"no token: an internal caller", Principal{}, false, AdminPolicy{}, ""},
		{"no token, new route", Principal{}, false, AdminPolicy{RefuseLegacy: true}, ""},
		{"owner with two-factor", twoFactor(RoleOwner), true, AdminPolicy{}, ""},
		{"admin with two-factor", twoFactor(RoleAdmin), true, AdminPolicy{RefuseLegacy: true}, ""},
		{"owner signed in without two-factor", user(RoleOwner, ViaSession), true, AdminPolicy{}, "second_factor_required"},
		{"admin signed in without two-factor", user(RoleAdmin, ViaSession), true, AdminPolicy{RefuseLegacy: true}, "second_factor_required"},
		{"the owner's device key", user(RoleOwner, ViaDevice), true, AdminPolicy{}, "admin_only"},
		{"the owner's personal token", user(RoleOwner, ViaToken), true, AdminPolicy{}, "admin_only"},
		{"the owner's MCP grant", user(RoleOwner, ViaMCP), true, AdminPolicy{}, "admin_only"},
		{"a device key claiming mfa", keyWithMFA, true, AdminPolicy{}, "admin_only"},
		{"member", twoFactor(RoleMember), true, AdminPolicy{}, "admin_only"},
		{"unknown role", twoFactor(""), true, AdminPolicy{}, "admin_only"},
		{"legacy access, a route from before accounts", user(RoleOwner, ViaLegacy), true, AdminPolicy{}, ""},
		{"legacy access, a new route", user(RoleOwner, ViaLegacy), true, AdminPolicy{RefuseLegacy: true}, "sign_in_required"},
		{"a delegation", Principal{Kind: KindDelegated, Role: RoleOwner}, true, AdminPolicy{}, "admin_only"},
		{"a service", Principal{Kind: KindService}, true, AdminPolicy{}, "admin_only"},
	}
	for _, tc := range cases {
		err := CheckAdmin(tc.principal, tc.ok, tc.policy)
		got := ""
		if err != nil {
			got = err.(*AdminError).Code
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAdminOnlyMiddleware(t *testing.T) {
	key := newKey(t, "auth-a")
	verifier := NewVerifier(keySet(t, key), func() time.Time { return now })
	app := fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error {
		status, code, message := err.(*AdminError).PublicError()
		return c.Status(status).JSON(map[string]any{"error": map[string]string{"code": code, "message": message}})
	}})
	app.Use(Middleware(verifier))
	app.Get("/v1/status", AdminOnly(AdminPolicy{}), func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/v1/diagnostics", AdminOnly(AdminPolicy{RefuseLegacy: true}), func(c fiber.Ctx) error { return c.SendString("ok") })
	call := func(path string, claims map[string]any) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if claims != nil {
			req.Header.Set(Header, sign(t, key, standardHeader(key.kid), claims))
		}
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, body
	}
	for _, tc := range []struct {
		path   string
		claims map[string]any
		status int
		code   string
	}{
		{"/v1/status", nil, 200, ""},
		{"/v1/status", twoFactorClaims(RoleOwner, ViaSession), 200, ""},
		{"/v1/status", twoFactorClaims(RoleAdmin, ViaSession), 200, ""},
		{"/v1/status", roleClaims(RoleOwner, ViaLegacy), 200, ""},
		{"/v1/status", roleClaims(RoleOwner, ViaSession), 403, "second_factor_required"},
		{"/v1/status", twoFactorClaims(RoleMember, ViaSession), 403, "admin_only"},
		// The mfa claim counts on a session only: on a key it is ignored.
		{"/v1/status", twoFactorClaims(RoleOwner, ViaDevice), 403, "admin_only"},
		{"/v1/status", twoFactorClaims(RoleOwner, ViaToken), 403, "admin_only"},
		{"/v1/status", twoFactorClaims(RoleOwner, ViaMCP), 403, "admin_only"},
		{"/v1/diagnostics", roleClaims(RoleOwner, ViaLegacy), 401, "sign_in_required"},
		{"/v1/diagnostics", twoFactorClaims(RoleMember, ViaSession), 403, "admin_only"},
		{"/v1/diagnostics", roleClaims(RoleAdmin, ViaSession), 403, "second_factor_required"},
		{"/v1/diagnostics", twoFactorClaims(RoleAdmin, ViaSession), 200, ""},
	} {
		status, body := call(tc.path, tc.claims)
		if status != tc.status {
			t.Errorf("%s %v: %d %s, want %d", tc.path, tc.claims["role"], status, body, tc.status)
			continue
		}
		if tc.code != "" && errorCode(t, body) != tc.code {
			t.Errorf("%s %v: %s, want %s", tc.path, tc.claims["role"], body, tc.code)
		}
	}
}

func TestAdminOnlyHandler(t *testing.T) {
	key := newKey(t, "auth-a")
	verifier := NewVerifier(keySet(t, key), func() time.Time { return now })
	handler := AdminOnlyHandler(verifier, AdminPolicy{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("status"))
	}))
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		if token != "" {
			req.Header.Set(Header, token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(""); rec.Code != 200 {
		t.Fatalf("an internal caller: %d", rec.Code)
	}
	if rec := call(sign(t, key, standardHeader(key.kid), roleClaims(RoleOwner, ViaLegacy))); rec.Code != 200 {
		t.Fatalf("legacy access: %d %s", rec.Code, rec.Body)
	}
	if rec := call(sign(t, key, standardHeader(key.kid), roleClaims(RoleMember, ViaSession))); rec.Code != 403 || errorCode(t, rec.Body.Bytes()) != "admin_only" {
		t.Fatalf("a member: %d %s", rec.Code, rec.Body)
	}
	if rec := call(sign(t, key, standardHeader(key.kid), twoFactorClaims(RoleOwner, ViaSession))); rec.Code != 200 {
		t.Fatalf("the owner with two-factor: %d %s", rec.Code, rec.Body)
	}
	if rec := call(sign(t, key, standardHeader(key.kid), roleClaims(RoleOwner, ViaSession))); rec.Code != 403 || errorCode(t, rec.Body.Bytes()) != "second_factor_required" {
		t.Fatalf("the owner without two-factor: %d %s", rec.Code, rec.Body)
	}
	if rec := call(sign(t, key, standardHeader(key.kid), roleClaims(RoleOwner, ViaDevice))); rec.Code != 403 || errorCode(t, rec.Body.Bytes()) != "admin_only" {
		t.Fatalf("the owner's device key: %d %s", rec.Code, rec.Body)
	}
	if rec := call("forged.token.value"); rec.Code != 401 || errorCode(t, rec.Body.Bytes()) != "unauthenticated" {
		t.Fatalf("a forged token: %d %s", rec.Code, rec.Body)
	}
	// A verifier without keys refuses every token, never lets one pass.
	closed := AdminOnlyHandler(NewVerifier(nil, nil), AdminPolicy{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set(Header, sign(t, key, standardHeader(key.kid), roleClaims(RoleOwner, ViaSession)))
	rec := httptest.NewRecorder()
	closed.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("without keys: %d", rec.Code)
	}
}
