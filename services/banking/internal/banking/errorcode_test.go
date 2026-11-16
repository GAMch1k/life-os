package banking_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/pkg/diagnostics"
	"github.com/GAMch1k/life-os/services/banking/internal/banking"
	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
)

// The provider's and Finance's answers are logged by status and code, never
// their message, which can name a bank, an account, or an amount
// (docs/architecture/multi-user.md#logs).
func TestProviderAndFinanceErrorsAreLoggedAsCodes(t *testing.T) {
	marker := "Deutsche Bank refused DE89 3704 0044 0532 0130 00, 12.34 EUR"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&enablebanking.APIError{Op: "transactions", Status: 429, Code: "ASPSP_RATE_LIMIT_EXCEEDED", Message: marker}, "http_429_aspsp_rate_limit_exceeded"},
		{&banking.SyncError{Kind: "bank", Message: marker, Err: &enablebanking.APIError{Status: 500, Message: marker}}, "http_500"},
		{fmt.Errorf("history: %w", &finance.StatusError{Status: 400, Code: "currency_mismatch", Message: marker}), "http_400_currency_mismatch"},
		{&finance.StatusError{Status: 503, Code: marker}, "http_503"},
	} {
		got := diagnostics.ErrorCode(tc.err)
		if got != tc.want || strings.Contains(got, "DE89") || strings.Contains(got, "12.34") {
			t.Errorf("ErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
