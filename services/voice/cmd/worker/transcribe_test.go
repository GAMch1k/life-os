package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A recording without speech is an outcome, not a Whisper failure: an empty
// transcript or one of only Whisper's non-speech annotations ends the job,
// while a malformed answer stays a failure to retry.
func TestTranscribeTellsNoSpeechFromAFailure(t *testing.T) {
	audio := filepath.Join(t.TempDir(), "audio.webm")
	if err := os.WriteFile(audio, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		want       string
		wantErr    error
		failure    bool
	}{
		{name: "speech", body: `{"transcript":" I already bought the chicken. "}`, want: "I already bought the chicken."},
		{name: "speech with an annotation", body: `{"transcript":"Buy milk (2 liters)"}`, want: "Buy milk (2 liters)"},
		{name: "Cyrillic speech", body: `{"transcript":"Купить молоко"}`, want: "Купить молоко"},
		{name: "empty", body: `{"transcript":""}`, wantErr: errNoSpeech},
		{name: "blank audio", body: `{"transcript":"[BLANK_AUDIO]"}`, wantErr: errNoSpeech},
		{name: "annotations only", body: `{"transcript":"[ Silence ] (music) *silence* ..."}`, wantErr: errNoSpeech},
		{name: "not JSON", body: `<html>`, failure: true},
		{name: "no transcript", body: `{}`, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer whisper.Close()
			got, err := transcribe(context.Background(), whisper.Client(), whisper.URL, audio, "audio/webm")
			switch {
			case tc.failure:
				if err == nil || errors.Is(err, errNoSpeech) {
					t.Fatalf("transcribe = %q, %v; want a failure to retry", got, err)
				}
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("transcribe = %q, %v; want %v", got, err, tc.wantErr)
				}
			default:
				if err != nil || got != tc.want {
					t.Fatalf("transcribe = %q, %v; want %q", got, err, tc.want)
				}
			}
		})
	}
}
