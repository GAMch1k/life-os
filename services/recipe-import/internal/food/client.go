// Package food saves recipes in the Food service through its public API
// (services/food/api/openapi.yaml: POST /v1/recipes, GET /v1/recipes), the
// only way recipe-import touches Food. The types mirror that contract;
// recipe-import never imports Food's code or reads its database.
package food

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/api/generated"
)

// Actor is who recipe-import's writes to Food are attributed to.
const Actor = "system:recipe-import"

// keyPrefix keeps the Idempotency-Keys passed on apart from Food's other
// clients' keys.
const keyPrefix = "recipe-import:"

// Ingredient is a recipe ingredient as Food takes it (NewIngredient).
type Ingredient struct {
	Name   string `json:"name"`
	Amount string `json:"amount,omitempty"`
}

// NewRecipe is a recipe as Food takes it (CreateRecipeRequest).
type NewRecipe struct {
	Title       string       `json:"title"`
	Servings    int          `json:"servings,omitempty"`
	Ingredients []Ingredient `json:"ingredients,omitempty"`
	Steps       string       `json:"steps,omitempty"`
	Tags        []string     `json:"tags,omitempty"`
	SourceURL   string       `json:"sourceUrl,omitempty"`
	Note        string       `json:"note,omitempty"`
}

// StatusError is an answer of Food other than success.
type StatusError struct {
	Status  int
	Code    string
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("Food returned %d %s: %s", e.Status, e.Code, e.Message)
}

// Is reports whether err is Food's answer with this status and code.
func Is(err error, status int, code string) bool {
	var answer *StatusError
	return errors.As(err, &answer) && answer.Status == status && (code == "" || answer.Code == code)
}

// Client calls Food at URL.
type Client struct {
	URL  string
	HTTP *http.Client
}

// New returns a client for the Food service at baseURL.
func New(baseURL string) *Client {
	return &Client{URL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// CreateRecipe saves a recipe. A recipe with the same title is Food's 409
// recipe_exists. key, when set, is passed on as the Idempotency-Key.
func (c *Client) CreateRecipe(ctx context.Context, recipe NewRecipe, key string) (generated.Recipe, error) {
	var out generated.Recipe
	headers := http.Header{}
	if key != "" {
		headers.Set("Idempotency-Key", keyPrefix+key)
	}
	err := c.do(ctx, http.MethodPost, "/v1/recipes", headers, recipe, &out)
	return out, err
}

// FindRecipe returns the recipe titled title as Food compares titles, or
// nil when there is none.
func (c *Client) FindRecipe(ctx context.Context, title string) (*generated.Recipe, error) {
	var list struct {
		Items []generated.Recipe `json:"items"`
	}
	query := url.Values{"q": {title}, "limit": {"50"}}
	if err := c.do(ctx, http.MethodGet, "/v1/recipes?"+query.Encode(), nil, nil, &list); err != nil {
		return nil, err
	}
	want := Normalize(title)
	for index := range list.Items {
		if list.Items[index].NormalizedTitle == want {
			return &list.Items[index], nil
		}
	}
	return nil, nil
}

// Normalize compares names as Food does: Unicode NFKC, lower case, "ё" as
// "е", runs of whitespace as one space, and surrounding punctuation
// stripped.
func Normalize(text string) string {
	text = strings.ReplaceAll(strings.ToLower(norm.NFKC.String(text)), "ё", "е")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) })
}

func (c *Client) do(ctx context.Context, method, path string, headers http.Header, body, out any) error {
	if c == nil || c.URL == "" {
		return errors.New("Food is not configured (FOOD_SERVICE_URL)")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.URL+path, reader)
	if err != nil {
		return err
	}
	for name, values := range headers {
		request.Header[name] = values
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	// The request's correlation and causation carry over; the actor is
	// always this integration.
	trace := activity.TraceFrom(ctx)
	trace.Actor = activity.Actor{}
	trace.Apply(request.Header)
	request.Header.Set(activity.ActorHeader, Actor)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return fmt.Errorf("Food could not be reached: %w", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		status := &StatusError{Status: response.StatusCode}
		var envelope struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			status.Code, status.Message = envelope.Error.Code, envelope.Error.Message
		}
		return status
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Food returned an unreadable answer: %w", err)
	}
	return nil
}
