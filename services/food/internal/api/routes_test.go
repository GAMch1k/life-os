package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
)

func TestWritesRequireAnActor(t *testing.T) {
	app := httpserver.New("food", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(app, NewHandler(nil, Options{}))
	const id = "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"
	writes := []struct{ method, target string }{
		{http.MethodPost, "/v1/shopping-items"}, {http.MethodPost, "/v1/shopping-items/clear-bought"},
		{http.MethodPatch, "/v1/shopping-items/" + id}, {http.MethodDelete, "/v1/shopping-items/" + id},
		{http.MethodPost, "/v1/shopping-items/" + id + "/bought"}, {http.MethodPost, "/v1/shopping-items/" + id + "/needed"},
		{http.MethodPost, "/v1/stores"}, {http.MethodPatch, "/v1/stores/" + id},
		{http.MethodPost, "/v1/aisle-rules"}, {http.MethodDelete, "/v1/aisle-rules/" + id},
		{http.MethodPost, "/v1/pantry-items"}, {http.MethodPatch, "/v1/pantry-items/" + id}, {http.MethodDelete, "/v1/pantry-items/" + id},
		{http.MethodPost, "/v1/pantry-items/" + id + "/used-up"}, {http.MethodPost, "/v1/pantry-items/" + id + "/thrown-away"},
		{http.MethodPost, "/v1/pantry-items/" + id + "/restock"},
	}
	write := func(method, target, actor string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(`{`))
		req.Header.Set("content-type", "application/json")
		if actor != "" {
			req.Header.Set(activity.ActorHeader, actor)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body.Error.Code
	}
	for _, route := range writes {
		for _, actor := range []string{"", "agent:", "owner"} {
			if status, code := write(route.method, route.target, actor); status != http.StatusBadRequest || code != activity.ActorRequiredCode {
				t.Errorf("%s %s as %q: %d %q, want 400 %s", route.method, route.target, actor, status, code, activity.ActorRequiredCode)
			}
		}
	}
	// With an actor the request reaches the handler, which rejects the
	// malformed body on its own terms.
	if status, code := write(http.MethodPost, "/v1/shopping-items", "dev:test"); status != http.StatusBadRequest || code != "invalid_request" {
		t.Errorf("declared actor: %d %q, want the handler's own 400 invalid_request", status, code)
	}
}
