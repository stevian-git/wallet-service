package app

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/problem"
)

type CreateAccountRequest struct {
	OwnerRef string             `json:"owner_ref"`
	Kind     ledger.AccountKind `json:"kind"`
	Currency string             `json:"currency"`
}

type TopupRequest struct {
	DestinationAccountID uuid.UUID       `json:"destination_account_id"`
	Amount               int64           `json:"amount"`
	Currency             string          `json:"currency"`
	Reference            *string         `json:"reference"`
	Metadata             json.RawMessage `json:"metadata"`
}

type TransferRequest struct {
	SourceAccountID      uuid.UUID       `json:"source_account_id"`
	DestinationAccountID uuid.UUID       `json:"destination_account_id"`
	Amount               int64           `json:"amount"`
	Currency             string          `json:"currency"`
	Reference            *string         `json:"reference"`
	Metadata             json.RawMessage `json:"metadata"`
}

type WithdrawalRequest struct {
	SourceAccountID uuid.UUID       `json:"source_account_id"`
	Amount          int64           `json:"amount"`
	Currency        string          `json:"currency"`
	Reference       *string         `json:"reference"`
	Metadata        json.RawMessage `json:"metadata"`
}

type ReversalRequest struct {
	Reason string `json:"reason"`
}

func validationError(detail string) error {
	return &problem.Error{
		Status: http.StatusUnprocessableEntity,
		Code:   "validation_failed",
		Title:  "Validation failed",
		Detail: detail,
	}
}

func requireUUID(id uuid.UUID, field string) error {
	if id == uuid.Nil {
		return validationError(field + " is required")
	}
	return nil
}

func normalizeMetadata(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, validationError("metadata must be a JSON object")
	}
	return trimmed, nil
}

type CreateHoldRequest struct {
	AccountID  uuid.UUID `json:"account_id"`
	Amount     int64     `json:"amount"`
	Currency   string    `json:"currency"`
	TTLSeconds int64     `json:"ttl_seconds"`
	Reference  *string   `json:"reference"`
}

type CaptureHoldRequest struct {
	DestinationAccountID uuid.UUID `json:"destination_account_id"`
	Amount               int64     `json:"amount"`
}
