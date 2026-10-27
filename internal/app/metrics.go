package app

import (
	"errors"
	"net/http"

	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
	"github.com/stevians/wallet-service/internal/problem"
)

func outcomeForTransfer(err error) string {
	switch {
	case err == nil:
		return "posted"
	case errors.Is(err, ledger.ErrInsufficientFunds):
		return "insufficient_funds"
	case errors.Is(err, ledger.ErrAccountFrozen):
		return "account_frozen"
	case errors.Is(err, ledger.ErrCurrencyMismatch):
		return "currency_mismatch"
	case errors.Is(err, ledger.ErrAlreadyReversed):
		return "already_reversed"
	case errors.Is(err, ledger.ErrNotFound):
		return "not_found"
	case errors.Is(err, ledger.ErrInvalidAmount),
		errors.Is(err, ledger.ErrInvalidPosting),
		errors.Is(err, ledger.ErrUnbalancedEntries):
		return "validation_failed"
	case errors.Is(err, idempotency.ErrKeyReused):
		return "idempotency_key_reused"
	case errors.Is(err, idempotency.ErrInProgress):
		return "idempotency_in_progress"
	case errors.Is(err, idempotency.ErrLeaseLost):
		return "idempotency_lease_lost"
	default:
		var problemErr *problem.Error
		if errors.As(err, &problemErr) {
			if problemErr.Status == http.StatusUnprocessableEntity {
				return "validation_failed"
			}
		}
		return "error"
	}
}

func recordTransferOutcome(transferType string, err error) {
	telemetry.RecordTransfer(transferType, outcomeForTransfer(err))
}
