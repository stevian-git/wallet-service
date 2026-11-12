package app

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
)

// postTransfer runs a money-moving ledger call under idempotency, records the
// transfer outcome metric, and renders the 201 response.
func (s *Service) postTransfer(
	ctx context.Context,
	meta Meta,
	kind ledger.TransferType,
	post func(ctx context.Context, tx pgx.Tx) (ledger.Transfer, error),
) (Result, error) {
	return s.do(ctx, meta, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		transfer, err := post(ctx, tx)
		recordTransferOutcome(string(kind), err)
		if err != nil {
			return idempotency.Response{}, err
		}
		return jsonResponse(http.StatusCreated, transferView(transfer))
	})
}

func (s *Service) Topup(ctx context.Context, meta Meta, req TopupRequest) (Result, error) {
	if err := requireUUID(req.DestinationAccountID, "destination_account_id"); err != nil {
		return Result{}, err
	}
	metadata, err := normalizeMetadata(req.Metadata)
	if err != nil {
		return Result{}, err
	}

	return s.postTransfer(ctx, meta, ledger.TransferTypeTopup, func(ctx context.Context, tx pgx.Tx) (ledger.Transfer, error) {
		return s.book.Topup(ctx, tx, ledger.TopupInput{
			DestinationAccountID: req.DestinationAccountID,
			Amount:               req.Amount,
			Currency:             req.Currency,
			Reference:            req.Reference,
			Metadata:             metadata,
		})
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

	return s.postTransfer(ctx, meta, ledger.TransferTypeTransfer, func(ctx context.Context, tx pgx.Tx) (ledger.Transfer, error) {
		return s.book.Transfer(ctx, tx, ledger.TransferInput{
			SourceAccountID:      req.SourceAccountID,
			DestinationAccountID: req.DestinationAccountID,
			Amount:               req.Amount,
			Currency:             req.Currency,
			Reference:            req.Reference,
			Metadata:             metadata,
		})
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

	return s.postTransfer(ctx, meta, ledger.TransferTypeWithdrawal, func(ctx context.Context, tx pgx.Tx) (ledger.Transfer, error) {
		return s.book.Withdrawal(ctx, tx, ledger.WithdrawalInput{
			SourceAccountID: req.SourceAccountID,
			Amount:          req.Amount,
			Currency:        req.Currency,
			Reference:       req.Reference,
			Metadata:        metadata,
		})
	})
}

func (s *Service) ReverseTransfer(ctx context.Context, meta Meta, transferID uuid.UUID, req ReversalRequest) (Result, error) {
	if len(req.Reason) > 255 {
		return Result{}, validationError("reason must be at most 255 characters")
	}

	return s.postTransfer(ctx, meta, ledger.TransferTypeReversal, func(ctx context.Context, tx pgx.Tx) (ledger.Transfer, error) {
		return s.book.Reverse(ctx, tx, transferID, req.Reason)
	})
}

func (s *Service) GetTransfer(ctx context.Context, transferID uuid.UUID) (TransferDetailView, error) {
	detail, err := s.book.GetTransfer(ctx, transferID)
	if err != nil {
		return TransferDetailView{}, err
	}
	return transferDetailView(detail), nil
}

func (s *Service) ListTransfers(ctx context.Context, cursor string, limit int) (TransfersPage, error) {
	afterID, err := parseIDCursor(cursor)
	if err != nil {
		return TransfersPage{}, err
	}
	transfers, next, err := s.book.ListTransfers(ctx, afterID, limit)
	if err != nil {
		return TransfersPage{}, err
	}
	return TransfersPage{Data: mapSlice(transfers, transferView), NextCursor: idCursor(next)}, nil
}
