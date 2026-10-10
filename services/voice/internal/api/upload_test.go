package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/voice/internal/config"
)

// uploadApp serves only the upload route, with the body limit the Voice
// server uses. Requests in these tests stop before storage and the database.
func uploadApp(maxUploadBytes int64) *httpserver.App {
	cfg := config.Config{MaxUploadBytes: maxUploadBytes}
	app := httpserver.New("voice", httpserver.WithFiberConfig(fiber.Config{BodyLimit: cfg.RequestBodyLimit()}))
	h := &Handler{maxUploadBytes: maxUploadBytes}
	app.Post("/v1/audio-jobs", h.CreateAudioJob)
	return app
}

func upload(t *testing.T, app *httpserver.App, audio []byte, source string) (int, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("audio", "voice-capture.webm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("source", source); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "/v1/audio-jobs", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return resp.StatusCode, envelope.Error.Code
}

// wav is a WAV header followed by size bytes of padding.
func wav(size int) []byte {
	return append([]byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00"), make([]byte, size)...)
}

func TestCreateAudioJobAcceptsRecordingsUpToTheConfiguredLimit(t *testing.T) {
	const limit = 25 << 20
	app := uploadApp(limit)
	// A recording over Fiber's 4 MiB default body limit passes validation
	// (the deliberately invalid source is rejected only afterwards).
	if status, _ := upload(t, app, wav(5<<20), "bogus"); status != http.StatusBadRequest {
		t.Fatalf("5 MiB WAV: status %d, want 400 for the invalid source", status)
	}
	ogg := append([]byte("OggS\x00\x02"), make([]byte, 20)...)
	ogg = append(ogg, 1, 19)
	ogg = append(ogg, []byte("OpusHead\x01\x01\x38\x01\x80\xbb\x00\x00\x00\x00\x00")...)
	if status, _ := upload(t, app, ogg, "bogus"); status != http.StatusBadRequest {
		t.Fatalf("Ogg/Opus: status %d, want 400 for the invalid source", status)
	}
}

func TestCreateAudioJobRejectsOversizedAndNonAudioUploads(t *testing.T) {
	const limit = 1 << 20
	app := uploadApp(limit)
	if status, code := upload(t, app, wav(limit), "web"); status != http.StatusRequestEntityTooLarge || code != "audio_too_large" {
		t.Fatalf("over-limit WAV: %d %q, want 413 audio_too_large", status, code)
	}
	if status, code := upload(t, app, []byte("<!doctype html><title>not audio</title>"), "web"); status != http.StatusBadRequest || code != "unsupported_audio" {
		t.Fatalf("HTML: %d %q, want 400 unsupported_audio", status, code)
	}
}
