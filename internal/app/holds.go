package app

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
)

func (s *Service) CreateHold(ctx context.Context, meta Meta, req CreateHoldRequest) (Result, error) {
	if err := requireUUID(req.AccountID, "account_id"); err != nil {
		return Result{}, err
	}
	if req.TTLSeconds < 1 || req.TTLSeconds > 604800 {
		return Result{}, validationError("ttl_seconds must be between 1 and 604800")
	}
	if req.Reference != nil && len(*req.Reference) > 255 {
		return Result{}, validationError("reference must be at most 255 characters")
	}

	return s.do(ctx, meta, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		hold, err := s.book.CreateHold(ctx, tx, ledger.CreateHoldInput{
			AccountID:  req.AccountID,
			Amount:     req.Amount,
			Currency:   req.Currency,
			TTLSeconds: req.TTLSeconds,
			Reference:  req.Reference,
		})
		if err != nil {
			return idempotency.Response{}, err
		}
		return jsonResponse(http.StatusCreated, holdView(hold))
	})
}

func (s *Service) GetHold(ctx context.Context, holdID uuid.UUID) (HoldView, error) {
	hold, err := s.book.GetHold(ctx, holdID)
	if err != nil {
		return HoldView{}, err
	}
	return holdView(hold), nil
}

func (s *Service) CaptureHold(ctx context.Context, meta Meta, holdID uuid.UUID, req CaptureHoldRequest) (Result, error) {
	if err := requireUUID(req.DestinationAccountID, "destination_account_id"); err != nil {
		return Result{}, err
	}
	if req.Amount < 0 {
		return Result{}, validationError("amount must be positive")
	}

	return s.postTransfer(ctx, meta, ledger.TransferTypeHoldCapture, func(ctx context.Context, tx pgx.Tx) (ledger.Transfer, error) {
		return s.book.CaptureHold(ctx, tx, ledger.CaptureHoldInput{
			HoldID:               holdID,
			DestinationAccountID: req.DestinationAccountID,
			Amount:               req.Amount,
		})
	})
}

func (s *Service) ReleaseHold(ctx context.Context, meta Meta, holdID uuid.UUID) (Result, error) {
	return s.do(ctx, meta, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		hold, err := s.book.ReleaseHold(ctx, tx, holdID)
		if err != nil {
			return idempotency.Response{}, err
		}
		return jsonResponse(http.StatusOK, holdView(hold))
	})
}
