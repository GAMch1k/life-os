package identity

import (
	"encoding/json"
	"net/http"
)

// Handler is Middleware for a plain net/http server (MCP's): a valid token
// puts its Principal in the request's context, an invalid one is refused
// with 401 unauthenticated in the services' error envelope, and a request
// without one passes. The paths in open are never checked.
func Handler(verifier *Verifier, next http.Handler, open ...string) http.Handler {
	skip := make(map[string]bool, len(open))
	for _, path := range open {
		skip[path] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(Header)
		if token == "" || skip[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		principal, err := verifier.Verify(token)
		if err != nil {
			status, code, message := err.(*Error).PublicError()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": code, "message": message},
			})
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}
