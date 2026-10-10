package delivery

import (
	"errors"
	"testing"
	"time"
)

var policy = Policy{BaseDelay: 10 * time.Second, MaxDelay: 15 * time.Minute, MaxAge: 24 * time.Hour}

func TestBackoffDoublesUpToTheCap(t *testing.T) {
	want := map[int32]time.Duration{
		0:       10 * time.Second,
		1:       10 * time.Second,
		2:       20 * time.Second,
		3:       40 * time.Second,
		4:       80 * time.Second,
		7:       640 * time.Second,
		8:       15 * time.Minute,
		40:      15 * time.Minute,
		1 << 30: 15 * time.Minute,
	}
	for attempt, delay := range want {
		if got := policy.Backoff(attempt); got != delay {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, delay)
		}
	}
}

func TestExpired(t *testing.T) {
	created := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	if policy.Expired(created, created.Add(24*time.Hour)) {
		t.Error("exactly the maximum age is not expired")
	}
	if !policy.Expired(created, created.Add(24*time.Hour+time.Second)) {
		t.Error("past the maximum age is expired")
	}
}

func TestAfterFailure(t *testing.T) {
	created := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	now := created.Add(time.Minute)
	transient := errors.New("connection refused")

	if got := policy.AfterFailure(3, created, now, transient); got.Status != StatusRetrying || !got.NextAttemptAt.Equal(now.Add(40*time.Second)) {
		t.Errorf("transient failure = %+v, want a retry in 40s", got)
	}
	if got := policy.AfterFailure(1, created, now, Permanent(transient)); got.Status != StatusFailed {
		t.Errorf("permanent failure = %+v, want failed", got)
	}
	if got := policy.AfterFailure(1, created, now, Skip("muted")); got.Status != StatusSkipped {
		t.Errorf("skip = %+v, want skipped", got)
	}
	late := created.Add(24*time.Hour - 5*time.Minute)
	if got := policy.AfterFailure(20, created, late, transient); got.Status != StatusFailed {
		t.Errorf("retry beyond the maximum age = %+v, want failed", got)
	}
}

func TestErrorClassification(t *testing.T) {
	base := errors.New("401 unauthorized")
	wrapped := Permanent(base)
	if !IsPermanent(wrapped) || !errors.Is(wrapped, base) || IsPermanent(base) {
		t.Error("Permanent must mark and wrap its error")
	}
	if Permanent(nil) != nil {
		t.Error("Permanent(nil) must be nil")
	}
	if !IsSkip(Skip("x")) || IsSkip(base) || IsPermanent(Skip("x")) {
		t.Error("Skip must be its own class")
	}
}

func TestBuildChannels(t *testing.T) {
	channels, err := Build([]string{"log"}, nil)
	if err != nil || len(channels) != 1 || channels[0].Name() != "log" {
		t.Fatalf("Build(log) = %v, %v", channels, err)
	}
	if _, err := Build([]string{"ntfy-typo"}, nil); err == nil {
		t.Fatal("an unknown channel must be an error")
	}
	if channels, err := Build(nil, nil); err != nil || len(channels) != 0 {
		t.Fatalf("Build(nil) = %v, %v", channels, err)
	}
}

func TestBoundError(t *testing.T) {
	long := make([]byte, 0, 2000)
	for range 2000 {
		long = append(long, 'e')
	}
	if got := boundError(errors.New(string(long))); len([]rune(got)) != maxErrorLength {
		t.Errorf("bounded length = %d", len([]rune(got)))
	}
	if got := boundError(errors.New("line one\n\tline two")); got != "line one line two" {
		t.Errorf("boundError = %q", got)
	}
}

func TestFormatAge(t *testing.T) {
	for d, want := range map[time.Duration]string{24 * time.Hour: "24h", 90 * time.Minute: "90m", 45 * time.Second: "45s"} {
		if got := formatAge(d); got != want {
			t.Errorf("formatAge(%v) = %q, want %q", d, got, want)
		}
	}
}
