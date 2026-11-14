package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const firstUser = "00000000-0000-4000-8000-000000000001"
const firstSpace = "00000000-0000-4000-8000-000000000002"

type testKey struct {
	kid     string
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func newKey(t *testing.T, kid string) testKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{kid: kid, private: private, public: public}
}

func (k testKey) jwk() map[string]string {
	return map[string]string{
		"kty": "OKP", "crv": "Ed25519", "kid": k.kid, "use": "sig", "alg": "EdDSA",
		"x": base64.RawURLEncoding.EncodeToString(k.public),
	}
}

func keySet(t *testing.T, keys ...testKey) *KeySet {
	t.Helper()
	list := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		list = append(list, key.jwk())
	}
	text, _ := json.Marshal(map[string]any{"keys": list})
	set, err := ParseKeySet(string(text))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func segment(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// sign makes a token as the Dashboard's check does (src/lib/auth/identity.ts).
func sign(t *testing.T, key testKey, header map[string]any, claims map[string]any) string {
	t.Helper()
	input := segment(t, header) + "." + segment(t, claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key.private, []byte(input)))
}

var now = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func standardHeader(kid string) map[string]any {
	return map[string]any{"alg": "EdDSA", "typ": "lifeos-id+jwt", "kid": kid}
}

func standardClaims() map[string]any {
	return map[string]any{
		"iss": "lifeos-auth", "aud": "lifeos-services",
		"sub": firstUser, "sid": "6a1f2c1e-2b0e-4f7a-9d55-2f1f1d7a0c11", "via": "session",
		"role": "owner", "sp": map[string]string{firstSpace: "o"}, "psp": firstSpace,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"jti": uuid.NewString(),
	}
}

func verifier(set *KeySet, at time.Time) *Verifier {
	return NewVerifier(set, func() time.Time { return at })
}

func TestVerifyAcceptsTheChecksToken(t *testing.T) {
	key := newKey(t, "auth-20261114")
	token := sign(t, key, standardHeader(key.kid), standardClaims())
	p, err := verifier(keySet(t, key), now.Add(time.Minute)).Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindUser || p.UserID.String() != firstUser || p.Via != ViaSession || p.Role != "owner" {
		t.Fatalf("principal = %+v", p)
	}
	if p.PersonalSpace.String() != firstSpace || p.Spaces[uuid.MustParse(firstSpace)] != "o" || len(p.Spaces) != 1 {
		t.Fatalf("spaces = %v, personal %v", p.Spaces, p.PersonalSpace)
	}
	if p.IsLegacy() || p.Scopes != nil || p.Modules != nil {
		t.Fatalf("principal = %+v", p)
	}
	if !p.ExpiresAt.Equal(now.Add(5 * time.Minute).Truncate(time.Second)) {
		t.Fatalf("expires %v", p.ExpiresAt)
	}
}

func TestVerifyRefuses(t *testing.T) {
	key := newKey(t, "auth-a")
	other := newKey(t, "auth-a")
	set := keySet(t, key)
	cases := map[string]struct {
		token string
		at    time.Time
	}{
		"expired": {
			sign(t, key, standardHeader(key.kid), standardClaims()),
			now.Add(5*time.Minute + Leeway + time.Second),
		},
		"not yet valid": {
			sign(t, key, standardHeader(key.kid), with(standardClaims(), "nbf", now.Add(2*time.Minute).Unix())),
			now,
		},
		"wrong audience": {
			sign(t, key, standardHeader(key.kid), with(standardClaims(), "aud", "svc:tasks")),
			now,
		},
		"wrong issuer": {
			sign(t, key, standardHeader(key.kid), with(standardClaims(), "iss", "https://lifeos.example.com")),
			now,
		},
		"no expiry": {
			sign(t, key, standardHeader(key.kid), without(standardClaims(), "exp")),
			now,
		},
		"alg none": {
			segment(t, map[string]any{"alg": "none", "typ": "lifeos-id+jwt", "kid": key.kid}) + "." + segment(t, standardClaims()) + ".",
			now,
		},
		"alg HS256": {
			sign(t, key, map[string]any{"alg": "HS256", "typ": "lifeos-id+jwt", "kid": key.kid}, standardClaims()),
			now,
		},
		"another type": {
			sign(t, key, map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": key.kid}, standardClaims()),
			now,
		},
		"unknown kid": {
			sign(t, key, standardHeader("auth-zzz"), standardClaims()),
			now,
		},
		"another key with the same kid": {
			sign(t, other, standardHeader(key.kid), standardClaims()),
			now,
		},
		"changed claims": {
			tamper(sign(t, key, standardHeader(key.kid), standardClaims()), with(standardClaims(), "role", "admin")),
			now,
		},
		"no subject": {
			sign(t, key, standardHeader(key.kid), without(standardClaims(), "sub")),
			now,
		},
		"unknown via": {
			sign(t, key, standardHeader(key.kid), with(standardClaims(), "via", "cookie")),
			now,
		},
		"bad space role": {
			sign(t, key, standardHeader(key.kid), with(standardClaims(), "sp", map[string]string{firstSpace: "x"})),
			now,
		},
		"not a JWS":  {"abc.def", now},
		"empty":      {"..", now},
		"huge token": {strings.Repeat("a", 9000), now},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := verifier(set, c.at).Verify(c.token)
			if err == nil {
				t.Fatal("accepted")
			}
			status, code, _ := err.(*Error).PublicError()
			if status != 401 || code != "unauthenticated" {
				t.Fatalf("%d %s", status, code)
			}
		})
	}
}

func TestVerifyLeeway(t *testing.T) {
	key := newKey(t, "auth-a")
	set := keySet(t, key)
	token := sign(t, key, standardHeader(key.kid), standardClaims())
	if _, err := verifier(set, now.Add(5*time.Minute+Leeway-time.Second)).Verify(token); err != nil {
		t.Fatalf("within the leeway after expiry: %v", err)
	}
	early := sign(t, key, standardHeader(key.kid), with(standardClaims(), "nbf", now.Add(20*time.Second).Unix()))
	if _, err := verifier(set, now).Verify(early); err != nil {
		t.Fatalf("within the leeway before nbf: %v", err)
	}
}

func TestRotationWithTwoKeys(t *testing.T) {
	old := newKey(t, "auth-old")
	fresh := newKey(t, "auth-new")
	both := keySet(t, old, fresh)
	for _, key := range []testKey{old, fresh} {
		if _, err := verifier(both, now).Verify(sign(t, key, standardHeader(key.kid), standardClaims())); err != nil {
			t.Fatalf("%s: %v", key.kid, err)
		}
	}
	onlyNew := keySet(t, fresh)
	if _, err := verifier(onlyNew, now).Verify(sign(t, old, standardHeader(old.kid), standardClaims())); err == nil {
		t.Fatal("a removed key still verifies")
	}
}

func TestLegacyAndArrayAudience(t *testing.T) {
	key := newKey(t, "auth-a")
	claims := with(with(standardClaims(), "via", "legacy"), "sid", "legacy")
	claims["aud"] = []string{"other", "lifeos-services"}
	p, err := verifier(keySet(t, key), now).Verify(sign(t, key, standardHeader(key.kid), claims))
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsLegacy() || p.Kind != KindUser {
		t.Fatalf("principal = %+v", p)
	}
}

func TestParseKeySet(t *testing.T) {
	key := newKey(t, "auth-a")
	good, _ := json.Marshal(map[string]any{"keys": []any{key.jwk()}})
	if set, err := ParseKeySet(string(good)); err != nil || set.Len() != 1 {
		t.Fatalf("%v %v", set, err)
	}
	if set, err := ParseKeySet("  "); err != nil || set.Len() != 0 {
		t.Fatalf("empty: %v %v", set, err)
	}
	private := key.jwk()
	private["d"] = base64.RawURLEncoding.EncodeToString(key.private.Seed())
	noKid := key.jwk()
	delete(noKid, "kid")
	rsa := key.jwk()
	rsa["kty"] = "RSA"
	shortX := key.jwk()
	shortX["x"] = "AAAA"
	for name, keys := range map[string]any{
		"a private key": []any{private},
		"no kid":        []any{noKid},
		"not Ed25519":   []any{rsa},
		"a bad x":       []any{shortX},
		"a kid twice":   []any{key.jwk(), key.jwk()},
	} {
		text, _ := json.Marshal(map[string]any{"keys": keys})
		if _, err := ParseKeySet(string(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseKeySet("{not json"); err == nil {
		t.Error("not JSON: accepted")
	}
}

// A token the Dashboard minted (testdata/dashboard-token.json, written by
// apps/dashboard/test/auth/write-go-fixture.mts): the two sides agree.
func TestVerifiesTheDashboardsTokens(t *testing.T) {
	raw, err := os.ReadFile("testdata/dashboard-token.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		TrustedKeys string `json:"trustedKeys"`
		Token       string `json:"token"`
		Now         int64  `json:"now"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	set, err := ParseKeySet(fixture.TrustedKeys)
	if err != nil {
		t.Fatal(err)
	}
	p, err := verifier(set, time.Unix(fixture.Now, 0)).Verify(fixture.Token)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID.String() != firstUser || p.Via != ViaSession || p.Role != "owner" || p.PersonalSpace.String() != firstSpace {
		t.Fatalf("principal = %+v", p)
	}
}

func with(claims map[string]any, key string, value any) map[string]any {
	copied := map[string]any{}
	for k, v := range claims {
		copied[k] = v
	}
	copied[key] = value
	return copied
}

func without(claims map[string]any, key string) map[string]any {
	copied := with(claims, key, nil)
	delete(copied, key)
	return copied
}

// tamper swaps a signed token's claims for others, keeping the signature.
func tamper(token string, claims map[string]any) string {
	parts := strings.Split(token, ".")
	raw, _ := json.Marshal(claims)
	parts[1] = base64.RawURLEncoding.EncodeToString(raw)
	return strings.Join(parts, ".")
}
