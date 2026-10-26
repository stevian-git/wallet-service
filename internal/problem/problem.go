package problem

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/stevians/wallet-service/internal/ledger"
)

const typeBase = "https://ledger.example/problems/"

type Error struct {
	Status int
	Code   string
	Title  string
	Detail string
	Slug   string
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Title, e.Detail)
	}
	return e.Title
}

func (e *Error) Replayable() bool {
	return e.Status >= http.StatusBadRequest && e.Status < http.StatusInternalServerError
}

func From(err error) *Error {
	var problemErr *Error
	if errors.As(err, &problemErr) {
		return problemErr
	}

	switch {
	case errors.Is(err, ledger.ErrNotFound):
		return &Error{Status: http.StatusNotFound, Code: "not_found", Title: "Not found"}
	case errors.Is(err, ledger.ErrInsufficientFunds):
		return &Error{Status: http.StatusUnprocessableEntity, Code: "insufficient_funds", Title: "Insufficient funds"}
	case errors.Is(err, ledger.ErrAccountFrozen):
		return &Error{Status: http.StatusUnprocessableEntity, Code: "account_frozen", Title: "Account frozen", Detail: err.Error()}
	case errors.Is(err, ledger.ErrCurrencyMismatch):
		return &Error{Status: http.StatusUnprocessableEntity, Code: "currency_mismatch", Title: "Currency mismatch"}
	case errors.Is(err, ledger.ErrAlreadyReversed):
		return &Error{Status: http.StatusConflict, Code: "already_reversed", Title: "Transfer already reversed"}
	case errors.Is(err, ledger.ErrHoldNotActive):
		return &Error{Status: http.StatusConflict, Code: "hold_not_active", Title: "Hold is not active", Detail: err.Error()}
	case errors.Is(err, ledger.ErrHoldExpired):
		return &Error{Status: http.StatusConflict, Code: "hold_expired", Title: "Hold expired"}
	case errors.Is(err, ledger.ErrAccountExists):
		return &Error{Status: http.StatusConflict, Title: "Account already exists", Slug: "account-exists"}
	case errors.Is(err, ledger.ErrInvalidAmount),
		errors.Is(err, ledger.ErrInvalidPosting),
		errors.Is(err, ledger.ErrUnbalancedEntries):
		return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Title: "Validation failed", Detail: err.Error()}
	default:
		return &Error{Status: http.StatusInternalServerError, Title: "Internal server error"}
	}
}

type Payload struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Code      string `json:"code,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func Marshal(err *Error, requestID string) []byte {
	payload := Payload{
		Type:      typeFor(err),
		Title:     err.Title,
		Status:    err.Status,
		Detail:    err.Detail,
		Code:      err.Code,
		RequestID: requestID,
	}
	body, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return []byte(`{"type":"about:blank","title":"Internal server error","status":500}`)
	}
	return body
}

func typeFor(err *Error) string {
	slug := err.Slug
	if slug == "" {
		slug = err.Code
	}
	if slug == "" {
		slug = "conflict"
	}
	if err.Status >= http.StatusInternalServerError {
		slug = "internal-error"
	}
	return typeBase + strings.ReplaceAll(slug, "_", "-")
}
