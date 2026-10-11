package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/inbox/internal/media"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/inbox/internal/testdb"
)

// The capture images of these tests are 4 KiB at most.
const testImageLimit = 4 << 10

type mediaAPI struct {
	t       *testing.T
	app     *httpserver.App
	pool    *pgxpool.Pool
	handler *Handler
	files   media.Store
	events  *fakeService
}

func newMediaAPI(t *testing.T) *mediaAPI {
	t.Helper()
	pool := testdb.Open(t, "inbox_media_test")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	events := newFakeService(t, nil)
	files := media.Store{Root: filepath.Join(t.TempDir(), "inbox-media")}
	handler := NewHandler(store.New(pool), activity.NewEmitter(events.url, "inbox", logger), nil).WithMedia(files, testImageLimit)
	app := httpserver.New("inbox", httpserver.WithLogger(logger))
	Register(app, handler)
	return &mediaAPI{t: t, app: app, pool: pool, handler: handler, files: files, events: events}
}

func (a *mediaAPI) send(request *http.Request) (int, http.Header, []byte) {
	a.t.Helper()
	response, err := a.app.Test(request, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		a.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, response.Header, raw
}

// upload posts images as the multipart field "images", as the owner.
func (a *mediaAPI) upload(actor string, images ...[]byte) (int, []byte) {
	a.t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for index, image := range images {
		part, err := form.CreateFormFile("images", "photo"+string(rune('a'+index))+".jpg")
		if err != nil {
			a.t.Fatal(err)
		}
		_, _ = part.Write(image)
	}
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/media", &body)
	request.Header.Set("content-type", form.FormDataContentType())
	if actor != "" {
		request.Header.Set(activity.ActorHeader, actor)
	}
	status, _, raw := a.send(request)
	return status, raw
}

func (a *mediaAPI) references(images ...[]byte) []string {
	a.t.Helper()
	status, raw := a.upload("user", images...)
	if status != http.StatusCreated {
		a.t.Fatalf("upload: %d %s", status, raw)
	}
	var list struct {
		Items []struct {
			Reference, Sha256, ContentType string
			ByteSize                       int64
		}
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		a.t.Fatal(err)
	}
	references := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		references = append(references, item.Reference)
	}
	return references
}

func (a *mediaAPI) capture(body map[string]any) (int, map[string]any) {
	a.t.Helper()
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, "/v1/inbox-items", bytes.NewReader(raw))
	request.Header.Set("content-type", "application/json")
	request.Header.Set(activity.ActorHeader, "user")
	status, _, out := a.send(request)
	var decoded map[string]any
	_ = json.Unmarshal(out, &decoded)
	return status, decoded
}

func errorCode(raw []byte) string {
	var envelope struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return envelope.Error.Code
}

// photo is a small JPEG-looking image whose bytes differ by seed.
func photo(seed byte, size int) []byte {
	data := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, bytes.Repeat([]byte{seed}, size)...)
	return data
}

func TestUploadedImagesAreStoredOnceAndServed(t *testing.T) {
	api := newMediaAPI(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 100)...)
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{2}, 100)...)
	status, raw := api.upload("user", photo(9, 500), png, webp)
	if status != http.StatusCreated {
		t.Fatalf("upload: %d %s", status, raw)
	}
	var list struct {
		Items []struct {
			Reference, Sha256, ContentType string
			ByteSize                       int64
		}
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Items) != 3 {
		t.Fatalf("list = %s, %v", raw, err)
	}
	for index, want := range []string{media.JPEG, media.PNG, media.WebP} {
		item := list.Items[index]
		if item.ContentType != want || item.Reference != "inbox-media:"+item.Sha256 || !media.ValidHash(item.Sha256) {
			t.Errorf("item %d = %+v, want %s", index, item, want)
		}
	}
	if list.Items[0].ByteSize != int64(len(photo(9, 500))) {
		t.Errorf("byte size = %d", list.Items[0].ByteSize)
	}

	// The same photo again: the same reference, stored once.
	again := api.references(photo(9, 500))
	if again[0] != list.Items[0].Reference {
		t.Fatalf("re-upload = %s, want %s", again[0], list.Items[0].Reference)
	}
	entries, _ := os.ReadDir(api.files.Root)
	if len(entries) != 3 {
		t.Fatalf("files = %d, want 3", len(entries))
	}

	// Served with its type, cached privately, never sniffed.
	request := httptest.NewRequest(http.MethodGet, "/v1/media/"+list.Items[1].Sha256, nil)
	status, header, body := api.send(request)
	if status != http.StatusOK || !bytes.Equal(body, png) || header.Get("content-type") != media.PNG ||
		header.Get("cache-control") != "private, max-age=31536000, immutable" || header.Get("x-content-type-options") != "nosniff" {
		t.Fatalf("get = %d %v (%d bytes)", status, header, len(body))
	}
	for target, want := range map[string]int{
		"/v1/media/" + strings.Repeat("0", 64): http.StatusNotFound,
		"/v1/media/not-a-hash":                 http.StatusBadRequest,
		"/v1/media/" + strings.Repeat("A", 64): http.StatusBadRequest,
	} {
		if status, _, raw := api.send(httptest.NewRequest(http.MethodGet, target, nil)); status != want {
			t.Errorf("GET %s = %d %s, want %d", target, status, raw, want)
		}
	}
}

func TestUploadLimitsAndFormats(t *testing.T) {
	api := newMediaAPI(t)
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	for name, tc := range map[string]struct {
		images [][]byte
		actor  string
		status int
		code   string
	}{
		"no actor":    {[][]byte{photo(1, 10)}, "", http.StatusBadRequest, activity.ActorRequiredCode},
		"no image":    {nil, "user", http.StatusBadRequest, "images_required"},
		"six images":  {[][]byte{photo(1, 10), photo(2, 10), photo(3, 10), photo(4, 10), photo(5, 10), photo(6, 10)}, "user", http.StatusBadRequest, "too_many_images"},
		"too large":   {[][]byte{photo(1, 10), photo(2, testImageLimit)}, "user", http.StatusRequestEntityTooLarge, "image_too_large"},
		"heic":        {[][]byte{heic}, "user", http.StatusBadRequest, "heic_not_supported"},
		"not image":   {[][]byte{photo(1, 10), []byte("just some text")}, "user", http.StatusBadRequest, "unsupported_image"},
		"empty image": {[][]byte{{}}, "user", http.StatusRequestEntityTooLarge, "image_too_large"},
	} {
		status, raw := api.upload(tc.actor, tc.images...)
		if status != tc.status || errorCode(raw) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", name, status, raw, tc.status, tc.code)
		}
	}
	// A refused upload stores nothing, not even its valid images.
	if entries, _ := os.ReadDir(api.files.Root); len(entries) != 0 {
		t.Fatalf("refused uploads left %d files", len(entries))
	}
	var rows int
	if err := api.pool.QueryRow(context.Background(), "SELECT count(*) FROM inbox_media").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("refused uploads left %d rows, %v", rows, err)
	}
	// Five images is the most one upload carries.
	if references := api.references(photo(1, 10), photo(2, 10), photo(3, 10), photo(4, 10), photo(5, 10)); len(references) != 5 {
		t.Fatalf("five images = %v", references)
	}
}

func TestCapturesNameOnlyUploadedImages(t *testing.T) {
	api := newMediaAPI(t)
	references := api.references(photo(1, 10), photo(2, 10))
	status, item := api.capture(map[string]any{"content": "Shared photo", "source": "web", "mediaReferences": append(references, references[0])})
	if status != http.StatusCreated {
		t.Fatalf("capture: %d %v", status, item)
	}
	stored, _ := item["mediaReferences"].([]any)
	if len(stored) != 2 || stored[0] != references[0] || stored[1] != references[1] {
		t.Fatalf("stored references = %v, want %v once each", stored, references)
	}
	// Its activity event counts the images.
	captured := api.events.ofType("inbox.item.captured")
	if data, _ := captured[0].body["data"].(map[string]any); len(captured) != 1 || data["images"] != float64(2) {
		t.Fatalf("events = %v", captured)
	}
	// A capture without images has an empty list, never null.
	if status, plain := api.capture(map[string]any{"content": "No photo", "source": "manual"}); status != http.StatusCreated || plain["mediaReferences"] == nil || len(plain["mediaReferences"].([]any)) != 0 {
		t.Fatalf("plain capture: %d %v", status, plain)
	}

	unknown := "inbox-media:" + strings.Repeat("0", 64)
	six := api.references(photo(3, 10), photo(4, 10), photo(5, 10))
	six = append(six, references...)
	six = append(six, api.references(photo(6, 10))...)
	for name, tc := range map[string]struct {
		references []string
		code       string
	}{
		"unknown":   {[]string{references[0], unknown}, "unknown_media"},
		"external":  {[]string{"https://example.com/a.jpg"}, "invalid_media_reference"},
		"malformed": {[]string{"inbox-media:../secret"}, "invalid_media_reference"},
		"six":       {six, "too_many_images"},
	} {
		raw, _ := json.Marshal(map[string]any{"content": "Shared " + name, "source": "web", "mediaReferences": tc.references})
		request := httptest.NewRequest(http.MethodPost, "/v1/inbox-items", bytes.NewReader(raw))
		request.Header.Set("content-type", "application/json")
		request.Header.Set(activity.ActorHeader, "user")
		status, _, out := api.send(request)
		if status != http.StatusBadRequest || errorCode(out) != tc.code {
			t.Errorf("%s: %d %s, want 400 %s", name, status, out, tc.code)
		}
	}
}

func TestOrphanedImagesArePrunedAfterADay(t *testing.T) {
	api := newMediaAPI(t)
	kept := api.references(photo(1, 10))[0]
	orphan := api.references(photo(2, 10))[0]
	fresh := api.references(photo(3, 10))[0]
	if status, item := api.capture(map[string]any{"content": "With a photo", "source": "web", "mediaReferences": []string{kept}}); status != http.StatusCreated {
		t.Fatalf("capture: %d %v", status, item)
	}
	ctx := context.Background()
	old := time.Now().Add(-25 * time.Hour)
	hash := func(reference string) string { h, _ := media.HashOf(reference); return h }
	if _, err := api.pool.Exec(ctx, "UPDATE inbox_media SET created_at = $1 WHERE sha256 = ANY($2)", old, []string{hash(kept), hash(orphan)}); err != nil {
		t.Fatal(err)
	}
	// A file left without a row by an interrupted upload, and a temporary
	// file, both a day old.
	stray := strings.Repeat("ab", 32)
	for _, name := range []string{stray, ".upload-123"} {
		path := api.files.Path(name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := api.handler.PruneMedia(ctx, time.Now())
	if err != nil || removed != 2 {
		t.Fatalf("pruned %d, %v; want the orphan and the stray file", removed, err)
	}
	for reference, want := range map[string]int{kept: http.StatusOK, fresh: http.StatusOK, orphan: http.StatusNotFound} {
		if status, _, _ := api.send(httptest.NewRequest(http.MethodGet, "/v1/media/"+hash(reference), nil)); status != want {
			t.Errorf("%s after pruning: %d, want %d", reference, status, want)
		}
	}
	for _, name := range []string{hash(orphan), stray, ".upload-123"} {
		if _, err := os.Stat(api.files.Path(name)); !os.IsNotExist(err) {
			t.Errorf("%s is still on disk: %v", name, err)
		}
	}
	// Uploading an orphan's bytes again makes it fresh: nothing to prune.
	api.references(photo(2, 10))
	if removed, err := api.handler.PruneMedia(ctx, time.Now()); err != nil || removed != 0 {
		t.Fatalf("second prune = %d, %v", removed, err)
	}
}

func TestMigration000009DownAndUp(t *testing.T) {
	api := newMediaAPI(t)
	api.references(photo(1, 10))
	ctx := context.Background()
	for _, name := range []string{"000009_capture_media.down.sql", "000009_capture_media.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var rows int
	if err := api.pool.QueryRow(ctx, "SELECT count(*) FROM inbox_media").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("after down and up: %d rows, %v", rows, err)
	}
	api.references(photo(2, 10))
}
