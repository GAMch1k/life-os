package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandler(t *testing.T) {
	key := newKey(t, "auth-a")
	verifier := NewVerifier(keySet(t, key), func() time.Time { return now })
	handler := Handler(verifier, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := FromContext(r.Context()); ok {
			_, _ = w.Write([]byte(p.UserID.String()))
			return
		}
		_, _ = w.Write([]byte("nobody"))
	}), "/health")
	call := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if token != "" {
			req.Header.Set(Header, token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("/mcp", ""); rec.Code != 200 || rec.Body.String() != "nobody" {
		t.Fatalf("without a token: %d %s", rec.Code, rec.Body)
	}
	if rec := call("/mcp", sign(t, key, standardHeader(key.kid), standardClaims())); rec.Code != 200 || rec.Body.String() != firstUser {
		t.Fatalf("with a token: %d %s", rec.Code, rec.Body)
	}
	rec := call("/mcp", "garbage")
	var envelope struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || rec.Code != 401 || envelope.Error.Code != "unauthenticated" {
		t.Fatalf("with a bad token: %d %s", rec.Code, rec.Body)
	}
	if rec := call("/health", "garbage"); rec.Code != 200 {
		t.Fatalf("/health with a bad token: %d", rec.Code)
	}
}
