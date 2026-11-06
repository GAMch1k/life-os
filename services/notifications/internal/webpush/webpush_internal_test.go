package webpush

import (
	"bytes"
	"crypto/ecdh"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The example of RFC 8291 Appendix A: with its keys and salt, Encrypt must
// produce its message byte for byte.
func TestEncryptReproducesTheRFC8291Example(t *testing.T) {
	decode := func(value string) []byte {
		t.Helper()
		raw, err := DecodeBase64URL(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	serverPrivate, err := ecdh.P256().NewPrivateKey(decode("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got := EncodeBase64URL(serverPrivate.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("as_public = %s", got)
	}
	body, err := encrypt(
		decode("V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"),
		decode("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		decode("BTBZMqHH6r4Tts7J_aSIgg"),
		serverPrivate,
		decode("DGv6ra1nlYgDCS1FRnbzlw"),
	)
	if err != nil {
		t.Fatal(err)
	}
	const want = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := EncodeBase64URL(body); got != want {
		t.Fatalf("encrypted message\n got %s\nwant %s", got, want)
	}
}

func TestEncryptRefusesWhatCannotBeSent(t *testing.T) {
	browser, _ := ecdh.P256().GenerateKey(nil)
	p256dh := browser.PublicKey().Bytes()
	auth := bytes.Repeat([]byte{1}, 16)
	if _, err := Encrypt(bytes.Repeat([]byte("x"), MaxPayload), p256dh, auth); err != nil {
		t.Fatalf("a payload of MaxPayload bytes: %v", err)
	}
	if body, _ := Encrypt(bytes.Repeat([]byte("x"), MaxPayload), p256dh, auth); len(body) != 4096 {
		t.Fatalf("the largest push is %d bytes, want 4096", len(body))
	}
	for name, call := range map[string]func() error{
		"too long":    func() error { _, err := Encrypt(bytes.Repeat([]byte("x"), MaxPayload+1), p256dh, auth); return err },
		"short auth":  func() error { _, err := Encrypt([]byte("x"), p256dh, auth[:8]); return err },
		"bad p256dh":  func() error { _, err := Encrypt([]byte("x"), p256dh[:33], auth); return err },
		"not a point": func() error { _, err := Encrypt([]byte("x"), bytes.Repeat([]byte{4}, 65), auth); return err },
	} {
		if call() == nil {
			t.Errorf("%s: encrypted", name)
		}
	}
}

func TestParseKeys(t *testing.T) {
	public, private, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(public) != 87 || len(private) != 43 {
		t.Fatalf("key lengths %d and %d, want 87 and 43 base64url characters", len(public), len(private))
	}
	keys, err := ParseKeys(public, private)
	if err != nil || keys.PublicKey() != public {
		t.Fatalf("ParseKeys = %v, %v", keys, err)
	}
	// Padding and surrounding space, as pasted, are tolerated.
	if _, err := ParseKeys(" "+public+"= ", private+"="); err != nil {
		t.Fatalf("padded keys: %v", err)
	}
	otherPublic, otherPrivate, _ := GenerateKeys()
	for name, pair := range map[string][2]string{
		"swapped":          {private, public},
		"another public":   {otherPublic, private},
		"another private":  {public, otherPrivate},
		"empty private":    {public, ""},
		"empty public":     {"", private},
		"not base64":       {public, "not base64!"},
		"truncated public": {public[:60], private},
	} {
		if _, err := ParseKeys(pair[0], pair[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidSubject(t *testing.T) {
	for subject, want := range map[string]bool{
		"mailto:owner@example.com":    true,
		"https://lifeos.example.com":  true,
		"https://lifeos.example.com/": true,
		"mailto:not an address":       false,
		"mailto:":                     false,
		"http://lifeos.example.com":   false,
		"lifeos.example.com":          false,
		"":                            false,
		"https://user@example.com":    false,
	} {
		if got := ValidSubject(subject); got != want {
			t.Errorf("ValidSubject(%q) = %v, want %v", subject, got, want)
		}
	}
}

func TestAuthorizationNamesTheOriginAndKey(t *testing.T) {
	public, private, _ := GenerateKeys()
	keys, _ := ParseKeys(public, private)
	now := time.Unix(1_760_000_000, 0)
	header, err := keys.authorization("https://fcm.googleapis.com/fcm/send/abc:def", "mailto:owner@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(header, "vapid t=") || !strings.HasSuffix(header, ", k="+public) {
		t.Fatalf("Authorization = %s", header)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(header, "vapid t="), ", k="+public)
	claims, _ := DecodeBase64URL(strings.Split(token, ".")[1])
	if want := `{"aud":"https://fcm.googleapis.com","exp":1760043200,"sub":"mailto:owner@example.com"}`; string(claims) != want {
		t.Fatalf("claims = %s, want %s", claims, want)
	}
	if _, err := keys.authorization("/relative", "mailto:owner@example.com", now); err == nil {
		t.Fatal("signed a token for a relative endpoint")
	}
}

func TestErrorClasses(t *testing.T) {
	for status, want := range map[int][2]bool{ // gone, retryable
		http.StatusNotFound:              {true, false},
		http.StatusGone:                  {true, false},
		http.StatusTooManyRequests:       {false, true},
		http.StatusRequestTimeout:        {false, true},
		http.StatusInternalServerError:   {false, true},
		http.StatusServiceUnavailable:    {false, true},
		http.StatusBadRequest:            {false, false},
		http.StatusUnauthorized:          {false, false},
		http.StatusForbidden:             {false, false},
		http.StatusRequestEntityTooLarge: {false, false},
		http.StatusFound:                 {false, false},
	} {
		err := &StatusError{StatusCode: status}
		if IsGone(err) != want[0] || IsRetryable(err) != want[1] {
			t.Errorf("%d: gone %v retryable %v, want %v", status, IsGone(err), IsRetryable(err), want)
		}
	}
	if !IsRetryable(errUnreachable) {
		t.Error("an unreachable push service is not retried")
	}
	if IsRetryable(invalidError{errUnreachable}) {
		t.Error("an invalid subscription is retried")
	}
}

// errUnreachable stands for a network error.
var errUnreachable = unreachableError{}

type unreachableError struct{}

func (unreachableError) Error() string { return "dial tcp: connection refused" }

func TestDeviceLabel(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36":                            "Chrome on Android",
		"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/28.0 Chrome/130.0.0.0 Mobile Safari/537.36": "Samsung Internet on Android",
		"Mozilla/5.0 (Android 14; Mobile; rv:131.0) Gecko/131.0 Firefox/131.0":                                                                       "Firefox on Android",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0":              "Edge on Windows",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15":                      "Safari on macOS",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1":    "Safari on iOS",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36":                                      "Chrome on Linux",
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36":                             "Chrome on ChromeOS",
		"LifeOS-Android/1.0.0 (1)":       "Life OS app on Android",
		"LifeOS-Android/1.2.3-beta (42)": "Life OS app on Android",
		"curl/8.5.0":                     "Unknown browser",
		"":                               "Unknown browser",
	} {
		if got := DeviceLabel(ua); got != want {
			t.Errorf("DeviceLabel(%q) = %q, want %q", ua, got, want)
		}
	}
}
