package app

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/pagination"
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

func (s *Service) ListAccounts(ctx context.Context, cursor string, limit int) (AccountsPage, error) {
	afterID, err := parseIDCursor(cursor)
	if err != nil {
		return AccountsPage{}, err
	}
	accounts, next, err := s.book.ListAccounts(ctx, afterID, limit)
	if err != nil {
		return AccountsPage{}, err
	}
	return AccountsPage{Data: mapSlice(accounts, accountView), NextCursor: idCursor(next)}, nil
}

func (s *Service) GetBalance(ctx context.Context, accountID uuid.UUID) (BalanceView, error) {
	balance, err := s.book.GetBalance(ctx, accountID)
	if err != nil {
		return BalanceView{}, err
	}
	return balanceView(balance), nil
}

func (s *Service) ListEntries(ctx context.Context, accountID uuid.UUID, cursor string, limit int) (EntriesPage, error) {
	afterID, err := decodeEntryCursor(cursor)
	if err != nil {
		return EntriesPage{}, err
	}
	entries, next, err := s.book.ListEntries(ctx, accountID, afterID, limit)
	if err != nil {
		return EntriesPage{}, err
	}

	var nextCursor *string
	if next != nil {
		value := pagination.EncodeInt64(*next)
		nextCursor = &value
	}
	return EntriesPage{Data: mapSlice(entries, entryView), NextCursor: nextCursor}, nil
}

// parseIDCursor reads the plain-UUID cursor used by the account and transfer
// lists.
// TODO: a malformed cursor returns the raw parse error, which problem.From maps
// to 500; it should be a 422 like the entries cursor.
func parseIDCursor(cursor string) (*uuid.UUID, error) {
	if cursor == "" {
		return nil, nil
	}
	id, err := uuid.Parse(cursor)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func idCursor(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	value := id.String()
	return &value
}
