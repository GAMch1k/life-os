package identity

import (
	"encoding/json"
	"net/http"

	"github.com/gofiber/fiber/v3"
)

// Server roles (the token's role claim).
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// IsAdmin reports whether p is a person whose server role is owner or admin.
// Legacy access is the first account, the owner, as Life OS was before
// accounts.
func (p Principal) IsAdmin() bool {
	return p.Kind == KindUser && (p.Role == RoleOwner || p.Role == RoleAdmin)
}

// AdminError refuses a request to an admin-only route
// (docs/architecture/multi-user.md#roles-and-checks).
type AdminError struct {
	Status  int
	Code    string
	Message string
}

func (e *AdminError) Error() string { return "admin route refused: " + e.Code }

// PublicError gives the HTTP answer (pkg/httpserver's error envelope).
func (e *AdminError) PublicError() (status int, code, message string) {
	return e.Status, e.Code, e.Message
}

var (
	errAdminOnly = &AdminError{Status: http.StatusForbidden, Code: "admin_only",
		Message: "Only the server's admins can do this."}
	errSignInRequired = &AdminError{Status: http.StatusUnauthorized, Code: "sign_in_required",
		Message: "Sign in with your account to do this."}
)

// AdminPolicy says who an admin-only route lets in.
type AdminPolicy struct {
	// RefuseLegacy refuses legacy access (a request without a credential):
	// for the admin routes that are new with accounts, such as diagnostics.
	// Routes that existed before accounts (backups, host-health, the
	// processor's status) let it in, as the owner, as before.
	RefuseLegacy bool
}

// CheckAdmin decides a request to an admin-only route from its principal.
// A request without one passes: in P21-03's step only an internal caller
// reaches a service without an identity token (the gateway authenticates
// every service route), such as the Dashboard rendering Status after its own
// admin check, or the monitor. P21-07 turns that into service assertions.
func CheckAdmin(p Principal, ok bool, policy AdminPolicy) error {
	if !ok {
		return nil
	}
	if p.Kind != KindUser {
		return errAdminOnly
	}
	if p.IsLegacy() && policy.RefuseLegacy {
		return errSignInRequired
	}
	if !p.IsAdmin() {
		return errAdminOnly
	}
	return nil
}

// AdminOnly is Fiber middleware for admin-only routes: a person who is the
// server's owner or an admin passes, any other principal is refused (403
// admin_only, or 401 sign_in_required for legacy access under RefuseLegacy).
// It reads the principal Middleware verified, so it must come after it
// (pkg/httpserver installs Middleware first).
func AdminOnly(policy AdminPolicy) fiber.Handler {
	return func(c fiber.Ctx) error {
		principal, ok := FromFiber(c)
		if err := CheckAdmin(principal, ok, policy); err != nil {
			return err
		}
		return c.Next()
	}
}

// AdminOnlyHandler is AdminOnly for a plain net/http handler: it verifies
// X-Life-OS-Identity itself (as Handler does), then applies the policy. A
// verifier without keys refuses every token.
func AdminOnlyHandler(verifier *Verifier, policy AdminPolicy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var principal Principal
		ok := false
		if token := r.Header.Get(Header); token != "" {
			verified, err := verifier.Verify(token)
			if err != nil {
				writeError(w, err.(*Error))
				return
			}
			principal, ok = verified, true
		}
		if err := CheckAdmin(principal, ok, policy); err != nil {
			writeError(w, err.(*AdminError))
			return
		}
		if ok {
			r = r.WithContext(WithPrincipal(r.Context(), principal))
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, err interface {
	PublicError() (status int, code, message string)
}) {
	status, code, message := err.PublicError()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
