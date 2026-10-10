// Package fakefinance is a Finance stand-in for tests: it records the bank
// imports and removals Banking sends, answers like Finance's bank import
// endpoints (counting transactions as new, updated, or unchanged by their
// external id), and can fail on demand.
package fakefinance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GAMch1k/life-os/services/banking/internal/finance"
)

// Server is the fake Finance.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	imports  []finance.Import
	actors   []string
	removed  []string
	known    map[string]finance.Transaction // connection/externalId
	accounts map[string]string              // connection/externalId -> id
	fail     []failure
}

type failure struct {
	method string
	status int
	code   string
}

// New starts a fake Finance.
func New(t testing.TB) *Server {
	s := &Server{known: map[string]finance.Transaction{}, accounts: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Fail makes the next request with method answer status and code.
func (s *Server) Fail(method string, status int, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = append(s.fail, failure{method: method, status: status, code: code})
}

// Imports returns the imports Finance accepted, oldest first.
func (s *Server) Imports() []finance.Import {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.imports)
}

// Actors returns the X-Life-OS-Actor of every request, in order.
func (s *Server) Actors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.actors)
}

// Removed returns the connection ids removed, in order.
func (s *Server) Removed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.removed)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actors = append(s.actors, r.Header.Get("X-Life-OS-Actor"))
	for i, f := range s.fail {
		if f.method == r.Method {
			s.fail = slices.Delete(s.fail, i, i+1)
			writeError(w, f.status, f.code)
			return
		}
	}
	if r.Header.Get("X-Life-OS-Actor") != finance.Actor {
		writeError(w, http.StatusBadRequest, "actor_required")
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/imports/bank":
		var request finance.Import
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		s.importSync(w, request)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/imports/bank/"):
		s.removed = append(s.removed, strings.TrimPrefix(r.URL.Path, "/v1/imports/bank/"))
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

func (s *Server) importSync(w http.ResponseWriter, request finance.Import) {
	if request.ConnectionID == "" || request.Window.From > request.Window.To {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	seen := map[string]bool{}
	var result finance.Result
	currencies := map[string]string{}
	for _, account := range request.Accounts {
		currencies[account.ExternalID] = account.Currency
		key := request.ConnectionID + "/" + account.ExternalID
		if _, ok := s.accounts[key]; ok {
			result.Accounts.Updated++
		} else {
			s.accounts[key] = fmt.Sprintf("acc-%d", len(s.accounts)+1)
			result.Accounts.Created++
		}
		result.AccountIDs = append(result.AccountIDs, struct {
			ExternalID string `json:"externalId"`
			ID         string `json:"id"`
		}{account.ExternalID, s.accounts[key]})
	}
	for _, transaction := range request.Transactions {
		if seen[transaction.ExternalID] {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		seen[transaction.ExternalID] = true
		if currency, ok := currencies[transaction.AccountExternalID]; !ok {
			writeError(w, http.StatusBadRequest, "unknown_account")
			return
		} else if currency != transaction.Amount.Currency {
			writeError(w, http.StatusBadRequest, "currency_mismatch")
			return
		}
	}
	for _, transaction := range request.Transactions {
		if transaction.Amount.Amount == 0 {
			result.Transactions.Skipped++
			continue
		}
		key := request.ConnectionID + "/" + transaction.ExternalID
		previous, ok := s.known[key]
		switch {
		case !ok:
			result.Transactions.Created++
		case reflect.DeepEqual(previous, transaction):
			result.Transactions.Unchanged++
		default:
			result.Transactions.Updated++
		}
		s.known[key] = transaction
	}
	s.imports = append(s.imports, request)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}, "request_id": "req-1"})
}
