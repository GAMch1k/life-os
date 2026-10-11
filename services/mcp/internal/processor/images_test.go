package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	jpegHash = strings.Repeat("a", 64)
	pngHash  = strings.Repeat("b", 64)
)

func TestPlanHandsTheCapturesImagesToCodexAndRemovesThem(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	copies := filepath.Join(dir, "copies")
	if err := os.Mkdir(copies, 0o700); err != nil {
		t.Fatal(err)
	}
	// The fake CLI records its arguments and keeps a copy of every image it
	// was given, as it was during the run.
	codex := fakeCodex(t, `printf '{"actions":[]}' > "$out"`)
	wrapper := filepath.Join(dir, "codex-images")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nprev=''\nfor arg in \"$@\"; do\n  if [ \"$prev\" = '--image' ]; then cp \"$arg\" " + copies + "/; fi\n  prev=$arg\ndone\nexec " + codex + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	images := CaptureImages{Images: []Image{{ContentType: "image/jpeg", Data: []byte("jpeg bytes")}, {ContentType: "image/png", Data: []byte("png bytes")}}, Omitted: 1}
	if _, err := (CodexCLI{Command: wrapper, WorkDir: workdir, Model: "gpt-5.6-luna"}).Plan(WithImages(context.Background(), images), "what is this?"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argsFile)
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var given []string
	lastImage, output := -1, -1
	for index, arg := range args {
		switch arg {
		case "--image":
			given = append(given, args[index+1])
			lastImage = index
		case "-o":
			output = index
		}
	}
	if len(given) != 2 || !strings.HasSuffix(given[0], "/image-1.jpg") || !strings.HasSuffix(given[1], "/image-2.png") {
		t.Fatalf("images given = %v in %q", given, args)
	}
	// Every --image comes before -o, which ends the list before the prompt.
	if output < 0 || lastImage > output {
		t.Fatalf("--image after -o in %q", args)
	}
	for _, path := range given {
		if filepath.Dir(filepath.Dir(path)) != workdir {
			t.Errorf("image %s is not in a directory of the workdir", path)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("image %s was not removed after the run: %v", path, err)
		}
	}
	for name, want := range map[string]string{"image-1.jpg": "jpeg bytes", "image-2.png": "png bytes"} {
		if got, err := os.ReadFile(filepath.Join(copies, name)); err != nil || string(got) != want {
			t.Errorf("%s during the run = %q, %v", name, got, err)
		}
	}
	if entries, _ := os.ReadDir(workdir); len(entries) != 0 {
		t.Fatalf("workdir keeps %v", entries)
	}
	prompt := args[len(args)-1]
	if !strings.Contains(prompt, "The item comes with 2 attached image(s), which are part of the capture") || !strings.Contains(prompt, "1 more image(s) of the capture could not be attached") {
		t.Fatalf("prompt lacks the images line: %q", prompt)
	}

	// Without images: no --image and no images line.
	if _, err := (CodexCLI{Command: wrapper, WorkDir: workdir, Model: "gpt-5.6-luna"}).Plan(context.Background(), "buy milk"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(argsFile)
	if strings.Contains(string(raw), "--image") || strings.Contains(string(raw), "attached image") {
		t.Fatalf("a capture without images got some: %q", raw)
	}
}

func TestInboxMediaReadsTheCapturesImagesWithinTheLimits(t *testing.T) {
	big := bytes.Repeat([]byte{1}, 15<<20)
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		switch strings.TrimPrefix(r.URL.Path, "/v1/media/") {
		case jpegHash:
			w.Header().Set("content-type", "image/jpeg")
			_, _ = w.Write([]byte("jpeg"))
		case pngHash:
			w.Header().Set("content-type", "image/png")
			_, _ = w.Write(big)
		case strings.Repeat("c", 64):
			w.Header().Set("content-type", "image/png")
			_, _ = w.Write(big) // over the 25 MiB total with the one before
		case strings.Repeat("d", 64):
			w.Header().Set("content-type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	media := InboxMedia{URL: server.URL + "/"}
	images, err := media.Fetch(context.Background(), []string{
		"inbox-media:" + jpegHash,
		"https://example.com/elsewhere.jpg", // not an Inbox image: ignored
		"inbox-media:" + pngHash,
		"inbox-media:" + strings.Repeat("c", 64),
		"inbox-media:" + strings.Repeat("d", 64),
		"inbox-media:" + strings.Repeat("e", 64), // gone
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(images.Images) != 2 || images.Images[0].ContentType != "image/jpeg" || string(images.Images[0].Data) != "jpeg" ||
		images.Images[1].ContentType != "image/png" || len(images.Images[1].Data) != len(big) || images.Omitted != 3 {
		t.Fatalf("images = %d (omitted %d)", len(images.Images), images.Omitted)
	}
	if len(requests) != 5 {
		t.Fatalf("requests = %v", requests)
	}

	// At most five images per run.
	many := make([]string, 0, 7)
	for range 7 {
		many = append(many, "inbox-media:"+jpegHash)
	}
	if images, err := media.Fetch(context.Background(), many); err != nil || len(images.Images) != 5 || images.Omitted != 2 {
		t.Fatalf("seven images = %d (omitted %d), %v", len(images.Images), images.Omitted, err)
	}

	// Inbox failing is an error: the capture waits for its images.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	if _, err := (InboxMedia{URL: failing.URL}).Fetch(context.Background(), []string{"inbox-media:" + jpegHash}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("failing Inbox: %v", err)
	}
}

type fakeMedia struct {
	images CaptureImages
	err    error
	asked  [][]string
}

func (m *fakeMedia) Fetch(_ context.Context, references []string) (CaptureImages, error) {
	m.asked = append(m.asked, references)
	return m.images, m.err
}

type imagePlanner struct{ seen []CaptureImages }

func (p *imagePlanner) Plan(ctx context.Context, _ string) (string, error) {
	p.seen = append(p.seen, imagesFrom(ctx))
	return `{"actions":[],"ambiguous":true,"confidence":0.1,"reason":"unclear"}`, nil
}

func TestRunnerPlansWithTheCapturesImages(t *testing.T) {
	references := []string{"inbox-media:" + jpegHash}
	tools := &corpusTools{item: inboxItem{ID: "item-1", Content: "Shared photo", Source: "web", MediaReferences: references, CreatedAt: time.Now()}}
	media := &fakeMedia{images: CaptureImages{Images: []Image{{ContentType: "image/jpeg", Data: []byte("x")}}}}
	planner := &imagePlanner{}
	if _, err := (Runner{Planner: planner, Tools: tools, Model: "gpt-5.6-luna", Media: media}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(media.asked) != 1 || media.asked[0][0] != references[0] || len(planner.seen) != 1 || len(planner.seen[0].Images) != 1 {
		t.Fatalf("asked %v, planned with %+v", media.asked, planner.seen)
	}

	// A capture without images does not ask Inbox for any.
	tools = &corpusTools{item: inboxItem{ID: "item-2", Content: "buy milk", Source: "manual", CreatedAt: time.Now()}}
	media = &fakeMedia{}
	if _, err := (Runner{Planner: &imagePlanner{}, Tools: tools, Model: "gpt-5.6-luna", Media: media}).Run(context.Background()); err != nil || len(media.asked) != 0 {
		t.Fatalf("asked %v, %v", media.asked, err)
	}
}

func TestRunnerRetriesWhenTheImagesAreUnavailable(t *testing.T) {
	tools := &corpusTools{item: inboxItem{ID: "item-1", Content: "Shared photo", Source: "web", MediaReferences: []string{"inbox-media:" + jpegHash}, CreatedAt: time.Now()}}
	planner := &imagePlanner{}
	media := &fakeMedia{err: context.DeadlineExceeded}
	if _, err := (Runner{Planner: planner, Tools: tools, Model: "gpt-5.6-luna", Media: media}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(planner.seen) != 0 {
		t.Fatal("planned without the capture's images")
	}
	last := tools.records[len(tools.records)-1]
	encoded, _ := json.Marshal(last)
	if last["status"] != "retryable" || !strings.Contains(string(encoded), "capture images unavailable") {
		t.Fatalf("recorded %s", encoded)
	}
}
