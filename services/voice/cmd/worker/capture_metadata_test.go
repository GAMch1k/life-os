package main

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
)

func TestCaptureMetadataCarriesTheUploadsMetadata(t *testing.T) {
	id := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true}
	job := store.AudioJob{ID: id, ContentType: "audio/webm", SourceMetadata: []byte(`{"timeZone":"America/New_York","audioJobId":"spoofed"}`)}
	want := map[string]any{"timeZone": "America/New_York", "audioJobId": "01020304-0506-0708-090a-0b0c0d0e0f10", "contentType": "audio/webm"}
	if got := captureMetadata(job); !reflect.DeepEqual(got, want) {
		t.Fatalf("captureMetadata = %v, want %v", got, want)
	}

	// Jobs uploaded without metadata (or stored as null) keep the job's keys.
	for _, raw := range []string{"", "null", "{}", "[1]"} {
		job.SourceMetadata = []byte(raw)
		got := captureMetadata(job)
		if len(got) != 2 || got["contentType"] != "audio/webm" || got["audioJobId"] != want["audioJobId"] {
			t.Errorf("metadata %q: captureMetadata = %v", raw, got)
		}
	}
}
