package banking

import (
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
)

// ExampleRedirectURI shows the form of LIFE_OS_BANKING_REDIRECT_URI: the
// Dashboard route the bank returns the owner to, under Life OS's public URL,
// as registered on the Enable Banking application. Compose derives it from
// LIFE_OS_PUBLIC_URL; there is no default here.
const ExampleRedirectURI = "https://lifeos.example.com/finance/bank/callback"

// Settings are the Enable Banking application and the key that seals
// session ids. The zero Settings is "not configured".
type Settings struct {
	AppID       string
	Key         *rsa.PrivateKey
	TokenKey    []byte
	RedirectURI string
	// APIURL is Enable Banking's API ("" is the real one).
	APIURL string
}

// Configured reports whether a bank can be connected.
func (s Settings) Configured() bool {
	return s.AppID != "" && s.Key != nil && len(s.TokenKey) == TokenKeySize && s.RedirectURI != ""
}

// Setting names, for messages (never their values).
const (
	EnvAppID    = "LIFE_OS_ENABLE_BANKING_APP_ID"
	EnvKeyFile  = "LIFE_OS_ENABLE_BANKING_KEY_FILE"
	EnvTokenKey = "LIFE_OS_BANKING_TOKEN_KEY"
	EnvRedirect = "LIFE_OS_BANKING_REDIRECT_URI"
)

// LoadSettings builds the settings from the environment's values, reading
// the private key from keyFile. With nothing set (or only an empty key
// file, as the verification stack mounts), banking is not configured and
// there is no error. A partial or invalid configuration also leaves it
// not configured and returns an error to log and show, naming the settings
// and never their values.
func LoadSettings(appID, keyFile, tokenKey, redirectURI, apiURL string) (Settings, error) {
	appID, keyFile, tokenKey = strings.TrimSpace(appID), strings.TrimSpace(keyFile), strings.TrimSpace(tokenKey)
	redirectURI = strings.TrimSpace(redirectURI)
	var keyData []byte
	if keyFile != "" {
		data, err := os.ReadFile(keyFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Settings{}, fmt.Errorf("Enable Banking is not configured: the file named by %s cannot be read (check its owner and mode)", EnvKeyFile)
		}
		keyData = []byte(strings.TrimSpace(string(data)))
	}
	if appID == "" && tokenKey == "" && len(keyData) == 0 {
		return Settings{}, nil
	}
	var missing []string
	if appID == "" {
		missing = append(missing, EnvAppID)
	}
	if len(keyData) == 0 {
		missing = append(missing, EnvKeyFile+" (an empty or missing key file)")
	}
	if tokenKey == "" {
		missing = append(missing, EnvTokenKey)
	}
	if redirectURI == "" {
		missing = append(missing, EnvRedirect)
	}
	if len(missing) > 0 {
		return Settings{}, fmt.Errorf("Enable Banking is not configured: %s %s not set", strings.Join(missing, " and "), plural(len(missing), "is", "are"))
	}
	key, err := enablebanking.ParsePrivateKey(keyData)
	if err != nil {
		return Settings{}, fmt.Errorf("Enable Banking is not configured: the key file named by %s is not an RSA private key in PEM", EnvKeyFile)
	}
	sealKey, err := ParseTokenKey(tokenKey)
	if err != nil {
		return Settings{}, fmt.Errorf("Enable Banking is not configured: %w", err)
	}
	if err := validRedirectURI(redirectURI); err != nil {
		return Settings{}, fmt.Errorf("Enable Banking is not configured: %w", err)
	}
	return Settings{AppID: appID, Key: key, TokenKey: sealKey, RedirectURI: redirectURI, APIURL: strings.TrimSpace(apiURL)}, nil
}

// ParseTokenKey decodes LIFE_OS_BANKING_TOKEN_KEY: 32 bytes in base64
// (standard or URL-safe, padded or not), as `openssl rand -base64 32` prints.
func ParseTokenKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := encoding.DecodeString(value); err == nil {
			if len(key) != TokenKeySize {
				return nil, fmt.Errorf("%s must be %d bytes in base64 (openssl rand -base64 32), not %d bytes", EnvTokenKey, TokenKeySize, len(key))
			}
			return key, nil
		}
	}
	return nil, fmt.Errorf("%s must be base64 (openssl rand -base64 32)", EnvTokenKey)
}

func validRedirectURI(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return fmt.Errorf("%s must be an absolute URL such as %s", EnvRedirect, ExampleRedirectURI)
	}
	local := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !local) {
		return fmt.Errorf("%s must use https (http only for localhost)", EnvRedirect)
	}
	return nil
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
