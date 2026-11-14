// Package identity verifies the identity tokens the gateway's auth check
// mints (docs/architecture/multi-user.md#the-identity-token) and tells a
// service who calls it.
//
// The token travels in one header, X-Life-OS-Identity: a compact JWS, EdDSA
// over Ed25519, signed by the Dashboard with LIFE_OS_IDENTITY_SIGNING_KEY.
// Services trust only the public keys LIFE_OS_TRUSTED_KEYS lists (pinned,
// never fetched), check the issuer, audience, type, and lifetime (30 s
// leeway), and put a Principal in the request's context.
//
// P21-03 is the first of two steps: a request with a token must carry a
// valid one (else 401 unauthenticated), and one without is still accepted,
// because only an internal caller reaches a service without one (the
// gateway authenticates every service route). P21-07 makes the token
// mandatory and adds service assertions and delegations.
package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// Header carries the identity token from the gateway to a service.
	Header = "X-Life-OS-Identity"
	// TrustedKeysEnv names the setting with the public keys services trust.
	TrustedKeysEnv = "LIFE_OS_TRUSTED_KEYS"

	issuer    = "lifeos-auth"
	audience  = "lifeos-services"
	tokenType = "lifeos-id+jwt"
	// Leeway for the clocks of the Dashboard and the service.
	Leeway = 30 * time.Second
	// maxTokenBytes bounds what is parsed; the check never mints over 2 KiB.
	maxTokenBytes = 8192
)

// Kind tells user calls from delegated and service calls.
type Kind string

const (
	// KindUser is a person: a session, or legacy access (and from P21-06 a
	// device key, a personal token, or an MCP grant).
	KindUser Kind = "user"
	// KindDelegated is a service acting for a person (P21-07).
	KindDelegated Kind = "delegated"
	// KindService is a service's own assertion (P21-07).
	KindService Kind = "service"
)

// How the caller proved who they are.
const (
	ViaSession    = "session"
	ViaLegacy     = "legacy"
	ViaDevice     = "device"
	ViaToken      = "token"
	ViaMCP        = "mcp"
	ViaDelegation = "delegation"
)

// Principal is who makes a request, from a verified identity token. A
// service never keeps one beyond its request.
type Principal struct {
	Kind Kind
	// UserID is the person (sub); uuid.Nil for a space-bound delegation.
	UserID uuid.UUID
	// Via is how they signed in: session, legacy, device, token, mcp, or
	// delegation.
	Via string
	// CredentialID is the session, key, grant, or "legacy" (sid).
	CredentialID string
	// Role is the server role: owner, admin, or member.
	Role string
	// Spaces are the spaces the person is in, with the role in each:
	// "o" owner, "a" admin, "m" member.
	Spaces map[uuid.UUID]string
	// PersonalSpace is the person's own space.
	PersonalSpace uuid.UUID
	// DefaultSpaces is where new items of a module go when not personal.
	DefaultSpaces map[string]uuid.UUID
	// Scopes limit a token, an MCP grant, or a delegation; nil is all of
	// the person's rights.
	Scopes []string
	// Modules are the optional modules allowed; nil is every installed one.
	Modules []string
	// Limits are the person's finite limits.
	Limits map[string]int64
	// Act is the service acting, on delegations only.
	Act string
	// TimeZone and Language are the person's, when the token has them.
	TimeZone string
	Language string
	// TokenID (jti) and ExpiresAt identify and bound the token.
	TokenID   string
	ExpiresAt time.Time
}

// IsLegacy reports whether the request came without any credential, under
// legacy access: the first account, as Life OS was before accounts.
func (p Principal) IsLegacy() bool { return p.Via == ViaLegacy }

// Error is a refused token. Its message names the problem for logs; the
// caller gets only "unauthenticated".
type Error struct{ Reason string }

func (e *Error) Error() string { return "identity token refused: " + e.Reason }

// PublicError gives the HTTP answer (pkg/httpserver's error envelope).
func (e *Error) PublicError() (status int, code, message string) {
	return 401, "unauthenticated", "The identity of this request could not be verified."
}

func refuse(format string, args ...any) error {
	return &Error{Reason: fmt.Sprintf(format, args...)}
}

var kidPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// KeySet holds the trusted public keys, by key id.
type KeySet struct {
	keys map[string]ed25519.PublicKey
}

// Len is the number of keys.
func (k *KeySet) Len() int {
	if k == nil {
		return 0
	}
	return len(k.keys)
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	D   string `json:"d"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

// ParseKeySet reads a JWKS of Ed25519 public keys ({"keys":[...]}), as
// LIFE_OS_TRUSTED_KEYS holds it. Empty text is an empty set (every token
// refused). A private key, another key type, or a missing kid is an error.
func ParseKeySet(text string) (*KeySet, error) {
	set := &KeySet{keys: map[string]ed25519.PublicKey{}}
	if strings.TrimSpace(text) == "" {
		return set, nil
	}
	var document struct {
		Keys []jwk `json:"keys"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%s is not a JWKS: %w", TrustedKeysEnv, err)
	}
	for _, key := range document.Keys {
		if key.Kty != "OKP" || key.Crv != "Ed25519" {
			return nil, fmt.Errorf("%s: key %q is not Ed25519 (kty OKP, crv Ed25519)", TrustedKeysEnv, key.Kid)
		}
		if !kidPattern.MatchString(key.Kid) {
			return nil, fmt.Errorf("%s: a key has no valid kid", TrustedKeysEnv)
		}
		if key.D != "" {
			return nil, fmt.Errorf("%s: key %q is a private key; list public keys only", TrustedKeysEnv, key.Kid)
		}
		raw, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s: key %q has no valid public key (x)", TrustedKeysEnv, key.Kid)
		}
		if _, dup := set.keys[key.Kid]; dup {
			return nil, fmt.Errorf("%s: key id %q is listed twice", TrustedKeysEnv, key.Kid)
		}
		set.keys[key.Kid] = ed25519.PublicKey(raw)
	}
	return set, nil
}

// KeySetFromEnv reads LIFE_OS_TRUSTED_KEYS.
func KeySetFromEnv() (*KeySet, error) {
	return ParseKeySet(os.Getenv(TrustedKeysEnv))
}

// Verifier checks identity tokens against a key set.
type Verifier struct {
	keys *KeySet
	now  func() time.Time
}

// NewVerifier verifies with keys; now is the clock (nil: time.Now).
func NewVerifier(keys *KeySet, now func() time.Time) *Verifier {
	if keys == nil {
		keys = &KeySet{keys: map[string]ed25519.PublicKey{}}
	}
	if now == nil {
		now = time.Now
	}
	return &Verifier{keys: keys, now: now}
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type claims struct {
	Iss  string            `json:"iss"`
	Aud  json.RawMessage   `json:"aud"`
	Sub  string            `json:"sub"`
	Sid  string            `json:"sid"`
	Via  string            `json:"via"`
	Act  string            `json:"act"`
	Role string            `json:"role"`
	Sp   map[string]string `json:"sp"`
	Psp  string            `json:"psp"`
	Dsp  map[string]string `json:"dsp"`
	Scp  []string          `json:"scp"`
	Mods []string          `json:"mods"`
	Lim  map[string]int64  `json:"lim"`
	Tz   string            `json:"tz"`
	Lng  string            `json:"lng"`
	Iat  *json.Number      `json:"iat"`
	Nbf  *json.Number      `json:"nbf"`
	Exp  *json.Number      `json:"exp"`
	Jti  string            `json:"jti"`
}

func decodeSegment(segment string, into any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(into)
}

func seconds(value *json.Number) (time.Time, bool) {
	if value == nil {
		return time.Time{}, false
	}
	n, err := value.Int64()
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

func audienceMatches(raw json.RawMessage) bool {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return one == audience
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, value := range many {
			if value == audience {
				return true
			}
		}
	}
	return false
}

var spaceRoles = map[string]bool{"o": true, "a": true, "m": true}
var serverRoles = map[string]bool{"owner": true, "admin": true, "member": true}

// Verify checks a token and returns its principal.
func (v *Verifier) Verify(token string) (Principal, error) {
	if len(token) > maxTokenBytes {
		return Principal{}, refuse("token too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Principal{}, refuse("not a compact JWS")
	}
	var h header
	if err := decodeSegment(parts[0], &h); err != nil {
		return Principal{}, refuse("unreadable header")
	}
	if h.Alg != "EdDSA" {
		return Principal{}, refuse("algorithm %q", h.Alg)
	}
	if h.Typ != tokenType {
		return Principal{}, refuse("type %q", h.Typ)
	}
	key, ok := v.keys.keys[h.Kid]
	if !ok {
		return Principal{}, refuse("unknown key %q", h.Kid)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return Principal{}, refuse("bad signature")
	}
	var c claims
	if err := decodeSegment(parts[1], &c); err != nil {
		return Principal{}, refuse("unreadable claims")
	}
	if c.Iss != issuer {
		return Principal{}, refuse("issuer %q", c.Iss)
	}
	if !audienceMatches(c.Aud) {
		return Principal{}, refuse("audience")
	}
	now := v.now()
	exp, ok := seconds(c.Exp)
	if !ok {
		return Principal{}, refuse("no expiry")
	}
	if now.After(exp.Add(Leeway)) {
		return Principal{}, refuse("expired")
	}
	if nbf, ok := seconds(c.Nbf); ok && now.Add(Leeway).Before(nbf) {
		return Principal{}, refuse("not yet valid")
	}
	return c.principal(exp)
}

func (c claims) principal(exp time.Time) (Principal, error) {
	p := Principal{
		Via:          c.Via,
		CredentialID: c.Sid,
		Role:         c.Role,
		Scopes:       c.Scp,
		Modules:      c.Mods,
		Limits:       c.Lim,
		Act:          c.Act,
		TimeZone:     c.Tz,
		Language:     c.Lng,
		TokenID:      c.Jti,
		ExpiresAt:    exp,
		Spaces:       map[uuid.UUID]string{},
	}
	switch c.Via {
	case ViaSession, ViaLegacy, ViaDevice, ViaToken, ViaMCP:
		p.Kind = KindUser
	case ViaDelegation:
		p.Kind = KindDelegated
	default:
		return Principal{}, refuse("via %q", c.Via)
	}
	if c.Sub != "" {
		id, err := uuid.Parse(c.Sub)
		if err != nil {
			return Principal{}, refuse("subject is not a UUID")
		}
		p.UserID = id
	}
	if p.Kind == KindUser && p.UserID == uuid.Nil {
		return Principal{}, refuse("no subject")
	}
	if c.Role != "" && !serverRoles[c.Role] {
		return Principal{}, refuse("role %q", c.Role)
	}
	for space, role := range c.Sp {
		id, err := uuid.Parse(space)
		if err != nil || !spaceRoles[role] {
			return Principal{}, refuse("spaces")
		}
		p.Spaces[id] = role
	}
	if c.Psp != "" {
		id, err := uuid.Parse(c.Psp)
		if err != nil {
			return Principal{}, refuse("personal space is not a UUID")
		}
		p.PersonalSpace = id
	}
	if len(c.Dsp) > 0 {
		p.DefaultSpaces = map[string]uuid.UUID{}
		for module, space := range c.Dsp {
			id, err := uuid.Parse(space)
			if err != nil {
				return Principal{}, refuse("default spaces")
			}
			p.DefaultSpaces[module] = id
		}
	}
	return p, nil
}

type contextKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// FromContext returns the request's principal; ok is false when the request
// came without an identity token (an internal caller, until P21-07).
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}
