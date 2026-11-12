package problem

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/reconciliation"
)

// These cases pin the wire contract (status, code, title, detail) so the
// mapping can be restructured without changing what clients see.
func TestFromMapsDomainErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
		title  string
		detail string
	}{
		{"not found", ledger.ErrNotFound, http.StatusNotFound, "not_found", "Not found", ""},
		{"insufficient", fmt.Errorf("wrap: %w", ledger.ErrInsufficientFunds), http.StatusUnprocessableEntity, "insufficient_funds", "Insufficient funds", ""},
		{"frozen", fmt.Errorf("%w: x", ledger.ErrAccountFrozen), http.StatusUnprocessableEntity, "account_frozen", "Account frozen", "account frozen: x"},
		{"exists", ledger.ErrAccountExists, http.StatusConflict, "", "Account already exists", ""},
		{"invalid posting", fmt.Errorf("%w: bad", ledger.ErrInvalidPosting), http.StatusUnprocessableEntity, "validation_failed", "Validation failed", "invalid posting: bad"},
		{"run not found", reconciliation.ErrRunNotFound, http.StatusNotFound, "not_found", "Not found", "reconciliation run not found"},
		{"file not found", reconciliation.ErrFileNotFound, http.StatusNotFound, "not_found", "Not found", "settlement file not found"},
		{"duplicate file", reconciliation.ErrDuplicateFile, http.StatusConflict, "duplicate_file", "Duplicate file", "settlement file with same sha256 already exists"},
		{"unknown", errors.New("boom"), http.StatusInternalServerError, "", "Internal server error", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := From(tc.err)
			if got.Status != tc.status || got.Code != tc.code || got.Title != tc.title || got.Detail != tc.detail {
				t.Fatalf("From(%v) = %+v, want status=%d code=%q title=%q detail=%q",
					tc.err, got, tc.status, tc.code, tc.title, tc.detail)
			}
		})
	}
}
