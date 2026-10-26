package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/problem"
)

type Service struct {
	book *ledger.Ledger
	idem *idempotency.Service
}

func New(book *ledger.Ledger, idem *idempotency.Service) *Service {
	return &Service{book: book, idem: idem}
}

type Meta struct {
	Scope     string
	Key       string
	Method    string
	Path      string
	RequestID string
	Body      []byte
}

type Result struct {
	StatusCode int
	Body       []byte
	Replayed   bool
}

func (s *Service) do(ctx context.Context, meta Meta, work idempotency.Work) (Result, error) {
	outcome, err := s.idem.Do(ctx, idempotency.Request{
		Scope:     meta.Scope,
		Key:       meta.Key,
		Method:    meta.Method,
		Path:      meta.Path,
		RequestID: meta.RequestID,
		Body:      meta.Body,
	}, work, errorMapper(meta.RequestID))
	if err != nil {
		return Result{}, mapIdempotencyError(err)
	}
	return Result{
		StatusCode: outcome.Response.StatusCode,
		Body:       outcome.Response.Body,
		Replayed:   outcome.Replayed,
	}, nil
}

func errorMapper(requestID string) idempotency.ErrorMapper {
	return func(err error) (idempotency.Response, bool) {
		problemErr := problem.From(err)
		if !problemErr.Replayable() {
			return idempotency.Response{}, false
		}
		return idempotency.Response{
			StatusCode: problemErr.Status,
			Body:       problem.Marshal(problemErr, requestID),
		}, true
	}
}

func mapIdempotencyError(err error) error {
	switch {
	case errors.Is(err, idempotency.ErrKeyReused):
		return &problem.Error{
			Status: http.StatusUnprocessableEntity,
			Code:   "idempotency_key_reused",
			Title:  "Idempotency key reused",
			Detail: "the same Idempotency-Key was used with a different request body",
		}
	case errors.Is(err, idempotency.ErrInProgress):
		return &problem.Error{
			Status: http.StatusConflict,
			Code:   "idempotency_in_progress",
			Title:  "Request already in progress",
		}
	case errors.Is(err, idempotency.ErrLeaseLost):
		return &problem.Error{
			Status: http.StatusConflict,
			Code:   "idempotency_in_progress",
			Title:  "Idempotency lease lost",
		}
	case errors.Is(err, idempotency.ErrInvalidKey):
		return &problem.Error{
			Status: http.StatusUnprocessableEntity,
			Code:   "validation_failed",
			Title:  "Validation failed",
			Detail: "Idempotency-Key is required and must be at most 255 characters",
		}
	default:
		return err
	}
}

func jsonResponse(status int, value any) (idempotency.Response, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{StatusCode: status, Body: body}, nil
}
