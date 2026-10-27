package app

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
)

func (s *Service) Topup(ctx context.Context, meta Meta, req TopupRequest) (Result, error) {
	if err := requireUUID(req.DestinationAccountID, "destination_account_id"); err != nil {
		return Result{}, err
	}
	metadata, err := normalizeMetadata(req.Metadata)
	if err != nil {
		return Result{}, err
	}

	return s.do(ctx, meta, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		transfer, err := s.book.Topup(ctx, tx, ledger.TopupInput{
			DestinationAccountID: req.DestinationAccountID,
			Amount:               req.Amount,
			Currency:             req.Currency,
			Reference:            req.Reference,
			Metadata:             metadata,
		})
		if err != nil {
			recordTransferOutcome(string(ledger.TransferTypeTopup), err)
			return idempotency.Response{}, err
		}
		recordTransferOutcome(string(ledger.TransferTypeTopup), nil)
		return jsonResponse(http.StatusCreated, transferView(transfer))
	})
}

func (s *Service) Transfer(ctx context.Context, meta Meta, req TransferRequest) (Result, error) {
	if err := requireUUID(req.SourceAccountID, "source_account_id"); err != nil {
		return Result{}, err
	}
	if err := requireUUID(req.DestinationAccountID, "destination_account_id"); err != nil {
		return Result{}, err
	}
	if req.SourceAccountID == req.DestinationAccountID {
		return Result{}, validationError("source_account_id and destination_account_id must differ")
	}
	metadata, err := normalizeMetadata(req.Metadata)
	if err != nil {
		return Result{}, err
	}

	return s.do(ctx, meta, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		transfer, err := s.book.Transfer(ctx, tx, ledger.TransferInput{
			SourceAccountID:      req.SourceAccountID,
			DestinationAccountID: req.DestinationAccountID,
			Amount:               req.Amount,
			Currency:             req.Currency,
			Reference:            req.Reference,
			Metadata:             metadata,
		})
		if err != nil {
			recordTransferOutcome(string(ledger.TransferTypeTransfer), err)
			return idempotency.Response{}, err
		}
		recordTransferOutcome(string(ledger.TransferTypeTransfer), nil)
		return jsonResponse(http.StatusCreated, transferView(transfer))
	})
}

func (s *Service) Withdrawal(ctx context.Context, meta Meta, req WithdrawalRequest) (Result, error) {
	if err := requireUUID(req.SourceAccountID, "source_account_id"); err != nil {
		return Result{}, err
	}
	metadata, err := normalizeMetadata(req.Metadata)
	if err != nil {
		return Result{}, err
	}

	return s.do(ctx, meta, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		transfer, err := s.book.Withdrawal(ctx, tx, ledger.WithdrawalInput{
			SourceAccountID: req.SourceAccountID,
			Amount:          req.Amount,
			Currency:        req.Currency,
			Reference:       req.Reference,
			Metadata:        metadata,
		})
		if err != nil {
			recordTransferOutcome(string(ledger.TransferTypeWithdrawal), err)
			return idempotency.Response{}, err
		}
		recordTransferOutcome(string(ledger.TransferTypeWithdrawal), nil)
		return jsonResponse(http.StatusCreated, transferView(transfer))
	})
}

func (s *Service) ReverseTransfer(ctx context.Context, meta Meta, transferID uuid.UUID, req ReversalRequest) (Result, error) {
	if len(req.Reason) > 255 {
		return Result{}, validationError("reason must be at most 255 characters")
	}

	return s.do(ctx, meta, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		transfer, err := s.book.Reverse(ctx, tx, transferID, req.Reason)
		if err != nil {
			recordTransferOutcome(string(ledger.TransferTypeReversal), err)
			return idempotency.Response{}, err
		}
		recordTransferOutcome(string(ledger.TransferTypeReversal), nil)
		return jsonResponse(http.StatusCreated, transferView(transfer))
	})
}

func (s *Service) GetTransfer(ctx context.Context, transferID uuid.UUID) (TransferDetailView, error) {
	detail, err := s.book.GetTransfer(ctx, transferID)
	if err != nil {
		return TransferDetailView{}, err
	}
	return transferDetailView(detail), nil
}
