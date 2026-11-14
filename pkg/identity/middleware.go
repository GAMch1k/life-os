package identity

import (
	"github.com/gofiber/fiber/v3"
)

type localsKey struct{}

// Middleware verifies X-Life-OS-Identity when a request has one: a valid
// token puts its Principal in the request (FromFiber, FromContext); an
// invalid one is refused with 401 unauthenticated. A request without one
// passes, unchanged (P21-03: only internal callers come without one).
// The paths in open, such as /health and /ready, are never checked.
func Middleware(verifier *Verifier, open ...string) fiber.Handler {
	skip := make(map[string]bool, len(open))
	for _, path := range open {
		skip[path] = true
	}
	return func(c fiber.Ctx) error {
		token := c.Get(Header)
		if token == "" || skip[c.Path()] {
			return c.Next()
		}
		principal, err := verifier.Verify(token)
		if err != nil {
			return err
		}
		c.Locals(localsKey{}, principal)
		c.SetContext(WithPrincipal(c.Context(), principal))
		return c.Next()
	}
}

// FromFiber returns the principal of a request Middleware verified.
func FromFiber(c fiber.Ctx) (Principal, bool) {
	principal, ok := c.Locals(localsKey{}).(Principal)
	return principal, ok
}
