package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
)

// Staples are the names the owner always wants at home; one marked running
// low, used up, or thrown away goes back on the shopping list by itself
// (Food's restock). The tools below are Food's staple operations; matching
// the owner's words to a staple or pantry name is the processor's.

// runningLow says something is running low, by name.
type runningLow struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	Name           string `json:"name" jsonschema:"The name, such as Milch: a staple's or pantry item's name as Food has it (compared normalised: Milch is not Vollmilch); for anything else the owner's words"`
	AddToList      bool   `json:"addToList,omitempty" jsonschema:"Also put it on the shopping list when it is no staple (a staple goes on the list by itself)"`
}

// stapleAdd makes a name a staple.
type stapleAdd struct {
	IdempotencyKey string  `json:"idempotencyKey,omitempty"`
	Name           string  `json:"name" jsonschema:"The staple as the owner wrote it, in their language and without the quantity, such as Milch or молоко"`
	Amount         *string `json:"amount,omitempty" jsonschema:"How much the shopping list gets, as free text, such as 2 l"`
	StoreName      *string `json:"storeName,omitempty" jsonschema:"The store the list gets, such as Lidl (case-insensitive); an unknown store is created"`
}

// stapleRemoval removes a staple, by name.
type stapleRemoval struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	Name           string `json:"name" jsonschema:"The staple's name, as food_list_staples lists it (compared normalised)"`
}

// stapleQuery lists the staples; it has no options.
type stapleQuery struct{}

const (
	// maxStapleName and maxStapleAmount are Food's limits.
	maxStapleName   = 200
	maxStapleAmount = 50
	maxStoreName    = 100
)

// addStapleTools registers the staple tools: listing, adding, and removing
// staples, and saying that something is running low.
func addStapleTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	client := coreclient.Client{BaseURL: baseURL, HTTP: hc}
	mcp.AddTool(s, &mcp.Tool{Name: "food_list_staples", Description: "List the staples, the names the owner always wants at home, by name: each with its amount, store, and aisle for the list, and whether an item of its name is needed on the list (onList), in stock (inStock), and flagged as running low (runningLow)"}, func(ctx context.Context, req *mcp.CallToolRequest, _ stapleQuery) (*mcp.CallToolResult, Output, error) {
		headers := http.Header{}
		traceOf(req).Apply(headers)
		return read(ctx, client, headers, "/v1/staples")
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_mark_running_low", Description: "Say that something is running low (\"almost out of milk\"), by name: every in-stock pantry item of the name is flagged, and a staple of the name goes on the shopping list by itself; addToList puts another name on the list too. Returns changed (whether anything was flagged or listed), staple, pantryItems, and restock (what went on the list: status added, merged, or on_list, and message; null when nothing did). A name that is neither a staple nor at home changes nothing"}, func(ctx context.Context, req *mcp.CallToolRequest, in runningLow) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_mark_running_low", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			name, err := stapleName(in.Name)
			if err != nil {
				return nil, Output{}, err
			}
			return post(ctx, hc, trace, baseURL, "/v1/staples/running-low", map[string]any{"name": name, "addToList": in.AddToList}, in.IdempotencyKey)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_add_staple", Description: "Make a name a staple, something the owner always wants at home: it goes back on the shopping list by itself whenever it runs low, is used up, or is thrown away. Optional amount and storeName (an unknown store is created) are what the list gets. Returns {created, staple}; created is false when the name already was a staple (that staple is returned unchanged)"}, func(ctx context.Context, req *mcp.CallToolRequest, in stapleAdd) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_add_staple", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			body, err := stapleAddBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			_, out, err := post(ctx, hc, trace, baseURL, "/v1/staples", body, in.IdempotencyKey)
			if err == nil {
				return nil, Output{Data: map[string]any{"created": true, "staple": out.Data}}, nil
			}
			if errorCode(err) != "staple_exists" {
				return nil, Output{}, err
			}
			headers := http.Header{}
			trace.Apply(headers)
			existing, findErr := stapleNamed(ctx, client, headers, body["name"].(string))
			if findErr != nil || existing == nil {
				return nil, Output{}, errors.Join(err, findErr)
			}
			return nil, Output{Data: map[string]any{"created": false, "staple": existing}}, nil
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_remove_staple", Description: "Stop a name being a staple, by its name (as food_list_staples lists it, compared normalised). What it put on the list stays. Returns {removed, staple}; removed is false when no staple has the name"}, func(ctx context.Context, req *mcp.CallToolRequest, in stapleRemoval) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_remove_staple", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			name, err := stapleName(in.Name)
			if err != nil {
				return nil, Output{}, err
			}
			headers := http.Header{}
			trace.Apply(headers)
			staple, err := stapleNamed(ctx, client, headers, name)
			if err != nil {
				return nil, Output{}, err
			}
			if staple == nil {
				return nil, Output{Data: map[string]any{"removed": false, "name": name}}, nil
			}
			id, err := uuid.Parse(fmt.Sprint(staple["id"]))
			if err != nil {
				return nil, Output{}, errors.New("Food listed a staple without an id")
			}
			if in.IdempotencyKey != "" {
				headers.Set("Idempotency-Key", in.IdempotencyKey)
			}
			removed := true
			// Food answers 204 with no body, or 404 when it is gone already.
			if _, err := client.DoWithHeaders(ctx, http.MethodDelete, "/v1/staples/"+id.String(), nil, headers); err != nil {
				if errorCode(err) != "not_found" {
					return nil, Output{}, err
				}
				removed = false
			}
			return nil, Output{Data: map[string]any{"removed": removed, "staple": staple}}, nil
		})
	})
}

// stapleName checks a staple or running-low name: not blank, at most
// maxStapleName characters, trimmed.
func stapleName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxStapleName {
		return "", fmt.Errorf("name must be 1 to %d characters", maxStapleName)
	}
	return name, nil
}

// stapleAddBody is the Food body for a new staple, checked before Food is
// called: its name, and the amount and store when given.
func stapleAddBody(in stapleAdd) (map[string]any, error) {
	name, err := stapleName(in.Name)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": name}
	setText(body, map[string]*string{"amount": in.Amount, "storeName": in.StoreName})
	if amount, ok := body["amount"].(string); ok && utf8.RuneCountInString(amount) > maxStapleAmount {
		return nil, fmt.Errorf("amount must be at most %d characters", maxStapleAmount)
	}
	if store, ok := body["storeName"].(string); ok && utf8.RuneCountInString(store) > maxStoreName {
		return nil, fmt.Errorf("storeName must be at most %d characters", maxStoreName)
	}
	return body, nil
}

// stapleNamed is the staple called name (compared with its normalised name
// like Food compares names, without case or extra spacing), or nil.
func stapleNamed(ctx context.Context, client coreclient.Client, headers http.Header, name string) (map[string]any, error) {
	_, out, err := read(ctx, client, headers, "/v1/staples")
	if err != nil {
		return nil, err
	}
	staples, _ := out.Data["items"].([]any)
	for _, raw := range staples {
		staple, _ := raw.(map[string]any)
		normalized, _ := staple["normalizedName"].(string)
		written, _ := staple["name"].(string)
		if sameName(normalized, name) || sameName(written, name) {
			return staple, nil
		}
	}
	return nil, nil
}

// errorCode is the code of a service's error answer, such as staple_exists,
// or "" for another error.
func errorCode(err error) string {
	var status coreclient.StatusError
	if !errors.As(err, &status) {
		return ""
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(status.Body, &envelope)
	return envelope.Error.Code
}
