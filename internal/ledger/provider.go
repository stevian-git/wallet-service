package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// providerOwnerRef is the owner_ref of a settlement provider's bank account.
func providerOwnerRef(provider string) string { return "provider:" + provider }

// EnsureProviderBankAccount returns the provider's bank account for currency,
// creating and linking it if it does not exist yet. The provider row must
// already exist in this transaction (the caller registers it first, which also
// locks it, so concurrent registrations cannot create two accounts).
//
// The account mirrors the shared system:bank account: debit-normal (an asset:
// money held at the provider) and allowed to go negative.
func (l *Ledger) EnsureProviderBankAccount(ctx context.Context, tx pgx.Tx, provider, currency string) (Account, bool, error) {
	if tx == nil {
		return Account{}, false, ErrNoTransaction
	}
	if !validCurrency(currency) {
		return Account{}, false, fmt.Errorf("%w: invalid currency %q", ErrInvalidPosting, currency)
	}

	existing, err := scanAccount(tx.QueryRow(ctx, `
		SELECT `+prefixedAccountColumns+`
		  FROM settlement_provider_accounts p
		  JOIN accounts a ON a.id = p.account_id
		 WHERE p.provider = $1 AND p.currency = $2`, provider, currency))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Account{}, false, err
	}

	var (
		id        pgtype.UUID
		createdAt time.Time
	)
	ownerRef := providerOwnerRef(provider)
	err = tx.QueryRow(ctx, `
		INSERT INTO accounts (kind, owner_ref, currency, normal_side, allow_negative)
		VALUES ('system_bank', $1, $2, 'debit', true)
		RETURNING id, created_at`, ownerRef, currency,
	).Scan(&id, &createdAt)
	if err != nil {
		return Account{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO account_balances (account_id, allow_negative) VALUES ($1, true)`, id); err != nil {
		return Account{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO settlement_provider_accounts (provider, currency, account_id)
		VALUES ($1, $2, $3)`, provider, currency, id); err != nil {
		return Account{}, false, fmt.Errorf("link provider account: %w", err)
	}

	accountID := uuidFromPg(id)
	if err := insertOutboxEvent(ctx, tx, "account", accountID, "account.created", map[string]any{
		"account_id": accountID,
		"owner_ref":  ownerRef,
		"currency":   currency,
	}); err != nil {
		return Account{}, false, err
	}

	return Account{
		ID:            accountID,
		Kind:          AccountKindSystemBank,
		OwnerRef:      &ownerRef,
		Currency:      currency,
		NormalSide:    NormalSideDebit,
		AllowNegative: true,
		Status:        accountStatusActive,
		CreatedAt:     createdAt,
	}, true, nil
}

func providerBankAccount(ctx context.Context, tx pgx.Tx, provider, currency string) (uuid.UUID, error) {
	var (
		id     pgtype.UUID
		status string
	)
	err := tx.QueryRow(ctx, `
		SELECT a.id, a.status
		  FROM settlement_provider_accounts p
		  JOIN accounts a ON a.id = p.account_id
		 WHERE p.provider = $1 AND p.currency = $2`, provider, currency,
	).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("%w: no %s account for provider %q", ErrUnknownProvider, currency, provider)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if status != accountStatusActive {
		return uuid.Nil, fmt.Errorf("%w: provider %q bank account is %s", ErrAccountFrozen, provider, status)
	}
	return uuidFromPg(id), nil
}
