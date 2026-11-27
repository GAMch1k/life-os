package email

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseOffWithoutHost(t *testing.T) {
	_, ok, problem := Parse(Settings{From: "lifeos@example.com", Password: "secret"})
	if ok || problem != "" {
		t.Fatalf("no host: ok=%v problem=%q, want off without a problem", ok, problem)
	}
}

func TestParseDefaults(t *testing.T) {
	for _, tc := range []struct {
		tls  string
		mode Mode
		port int
	}{
		{"", ModeSTARTTLS, 587},
		{"starttls", ModeSTARTTLS, 587},
		{"TLS", ModeTLS, 465},
		{"none", ModeNone, 25},
	} {
		config, ok, problem := Parse(Settings{Host: "smtp.example.com", TLS: tc.tls, From: "lifeos@example.com", LinkBaseURL: "https://lifeos.example.com/"})
		if !ok {
			t.Fatalf("%q: %s", tc.tls, problem)
		}
		if config.Mode != tc.mode || config.Port != tc.port || config.FromName != "Life OS" {
			t.Errorf("%q: %+v", tc.tls, config)
		}
		if config.HELO != "lifeos.example.com" || config.ServerURL != "https://lifeos.example.com" {
			t.Errorf("%q: HELO %q, server %q", tc.tls, config.HELO, config.ServerURL)
		}
	}
}

func TestParseRefusesSettingsThatCannotWork(t *testing.T) {
	base := Settings{Host: "smtp.example.com", From: "lifeos@example.com"}
	for name, change := range map[string]func(*Settings){
		"a host with a space":       func(s *Settings) { s.Host = "smtp example.com" },
		"a URL as host":             func(s *Settings) { s.Host = "smtp://smtp.example.com" },
		"an unknown TLS mode":       func(s *Settings) { s.TLS = "ssl" },
		"a port out of range":       func(s *Settings) { s.Port = "70000" },
		"a port that is no number":  func(s *Settings) { s.Port = "smtp" },
		"no From":                   func(s *Settings) { s.From = "" },
		"a From with a name":        func(s *Settings) { s.From = "Life OS <lifeos@example.com>" },
		"two From addresses":        func(s *Settings) { s.From = "a@example.com,b@example.com" },
		"a password without a user": func(s *Settings) { s.Password = "hunter2hunter2" },
		"a user without TLS":        func(s *Settings) { s.TLS = "none"; s.Username = "relay"; s.Password = "hunter2hunter2" },
		"a From name on two lines":  func(s *Settings) { s.FromName = "Life\nOS" },
		"a password on two lines":   func(s *Settings) { s.Username = "u"; s.Password = "hunter2\r\nRCPT TO:<x>" },
	} {
		settings := base
		change(&settings)
		_, ok, problem := Parse(settings)
		if ok || problem == "" {
			t.Errorf("%s: ok=%v problem=%q", name, ok, problem)
		}
		if settings.Password != "" && strings.Contains(problem, settings.Password) {
			t.Errorf("%s: the problem shows the password", name)
		}
	}
}

func TestConfigNeverShowsThePassword(t *testing.T) {
	config, ok, problem := Parse(Settings{Host: "smtp.example.com", Username: "apikey", Password: "very-secret-value", From: "lifeos@example.com"})
	if !ok {
		t.Fatal(problem)
	}
	holder := struct{ config Config }{config}
	for _, text := range []string{config.String(), fmt.Sprint(config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config), fmt.Sprintf("%#v", holder), fmt.Sprintf("%+v", holder)} {
		if strings.Contains(text, "very-secret-value") {
			t.Fatalf("the password shows: %s", text)
		}
	}
	if config.Password() != "very-secret-value" {
		t.Fatal("the sender cannot read the password")
	}
}

func TestParseAddress(t *testing.T) {
	for _, good := range []string{"you@example.com", " you+tag@sub.example.co.uk ", "a@b"} {
		if _, err := ParseAddress(good); err != nil {
			t.Errorf("%q refused", good)
		}
	}
	for _, bad := range []string{"", "you", "@example.com", "you@", "You <you@example.com>", "a@b.c, d@e.f", "you@example.com\r\nBcc: x@y.z", "you @example.com"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHELOName(t *testing.T) {
	for url, want := range map[string]string{
		"https://lifeos.example.com":       "lifeos.example.com",
		"https://LifeOS.Example.com:8443/": "lifeos.example.com",
		"http://127.0.0.1:3000":            "localhost",
		"http://lifeos":                    "localhost",
		"":                                 "localhost",
	} {
		if got := heloName(url); got != want {
			t.Errorf("%q: %q, want %q", url, got, want)
		}
	}
}
