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
	app := httpserver.New("calendar", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(app, NewHandler(nil))
	write := func(actor string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(`{`))
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
	for _, actor := range []string{"", "agent:", "owner"} {
		if status, code := write(actor); status != http.StatusBadRequest || code != activity.ActorRequiredCode {
			t.Errorf("actor %q: %d %q, want 400 %s", actor, status, code, activity.ActorRequiredCode)
		}
	}
	// With an actor the request reaches the handler, which rejects the
	// incomplete body on its own terms.
	if status, code := write("dev:test"); status != http.StatusBadRequest || code == activity.ActorRequiredCode {
		t.Errorf("declared actor: %d %q, want the handler's own 400", status, code)
	}
}
