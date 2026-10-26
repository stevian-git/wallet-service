package app

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
)

func (s *Service) CreateAccount(ctx context.Context, meta Meta, req CreateAccountRequest) (Result, error) {
	if req.Kind == "" {
		req.Kind = ledger.AccountKindUserWallet
	}

	return s.do(ctx, meta, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		account, err := s.book.CreateAccount(ctx, tx, ledger.CreateAccountInput{
			OwnerRef: req.OwnerRef,
			Kind:     req.Kind,
			Currency: req.Currency,
		})
		if err != nil {
			return idempotency.Response{}, err
		}
		return jsonResponse(http.StatusCreated, accountView(account))
	})
}

func (s *Service) GetAccount(ctx context.Context, accountID uuid.UUID) (AccountView, error) {
	account, err := s.book.GetAccount(ctx, accountID)
	if err != nil {
		return AccountView{}, err
	}
	return accountView(account), nil
}

func (s *Service) GetBalance(ctx context.Context, accountID uuid.UUID) (BalanceView, error) {
	balance, err := s.book.GetBalance(ctx, accountID)
	if err != nil {
		return BalanceView{}, err
	}
	return balanceView(balance), nil
}

func (s *Service) ListEntries(ctx context.Context, accountID uuid.UUID, cursor string, limit int) (EntriesPage, error) {
	afterID, err := decodeCursor(cursor)
	if err != nil {
		return EntriesPage{}, err
	}

	entries, next, err := s.book.ListEntries(ctx, accountID, afterID, limit)
	if err != nil {
		return EntriesPage{}, err
	}

	var nextCursor *string
	if next != nil {
		value := encodeCursor(*next)
		nextCursor = &value
	}

	return EntriesPage{Data: entryViews(entries), NextCursor: nextCursor}, nil
}
