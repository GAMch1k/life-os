package identity

import (
	"net/http"
	"slices"

	"github.com/gofiber/fiber/v3"
)

// Scopes of personal API tokens and MCP grants
// (docs/architecture/multi-user.md#roles-and-checks): "<module>:read", or
// "<module>:write", which includes reading. A token without scopes (a
// session, a device key, legacy access) has all of the person's rights.

// ScopeError refuses a request its token's scopes do not cover.
type ScopeError struct{}

func (e *ScopeError) Error() string { return "insufficient scope" }

// PublicError gives the HTTP answer (pkg/httpserver's error envelope).
func (e *ScopeError) PublicError() (status int, code, message string) {
	return http.StatusForbidden, "insufficient_scope", "This token may not do this: see its scopes in Settings → API tokens."
}

// Reads reports whether a method only reads.
func Reads(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// Allows reports whether p's scopes allow reading (or, with write, changing)
// module. A principal without scopes is allowed everything its other checks
// allow; one with scopes needs "<module>:write", or "<module>:read" to read.
func (p Principal) Allows(module string, write bool) bool {
	if p.Scopes == nil {
		return true
	}
	if module == "" {
		return false
	}
	if slices.Contains(p.Scopes, module+":write") {
		return true
	}
	return !write && slices.Contains(p.Scopes, module+":read")
}

// ServiceModule is the module whose scopes a service's routes need: the
// domain services their own, the integrations the module they feed. The
// server's own services (backup, host-health, monitor) have none, so no
// scoped token reaches them.
func ServiceModule(service string) string {
	switch service {
	case "finance", "tasks", "inbox", "calendar", "food", "activity", "notifications", "voice":
		return service
	case "banking":
		return "finance"
	case "google-calendar", "calendar-feeds":
		return "calendar"
	case "recipe-import":
		return "food"
	}
	return ""
}

// RequireScope is Fiber middleware that refuses a scoped token a route of
// module it is not scoped for (403 insufficient_scope): reading needs
// "<module>:read" or ":write", any other method ":write". It reads the
// principal Middleware verified, so it must come after it (pkg/httpserver
// installs both). The paths in open are never checked.
func RequireScope(module string, open ...string) fiber.Handler {
	skip := make(map[string]bool, len(open))
	for _, path := range open {
		skip[path] = true
	}
	return func(c fiber.Ctx) error {
		if skip[c.Path()] {
			return c.Next()
		}
		principal, ok := FromFiber(c)
		if ok && !principal.Allows(module, !Reads(c.Method())) {
			return &ScopeError{}
		}
		return c.Next()
	}
}
