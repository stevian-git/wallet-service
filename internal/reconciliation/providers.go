package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	_ "time/tzdata" // provider cut-off zones must load in images without tzdata

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrInvalidProvider wraps every validation problem of a provider registration.
	ErrInvalidProvider  = errors.New("invalid settlement provider")
	ErrProviderNotFound = errors.New("settlement provider not found")

	providerNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	currencyPattern     = regexp.MustCompile(`^[A-Z]{3}$`)
)

// ValidateProviderInput checks a registration before anything is written.
func ValidateProviderInput(in PutProviderInput) error {
	if !providerNamePattern.MatchString(in.Provider) {
		return fmt.Errorf("%w: provider must match %s", ErrInvalidProvider, providerNamePattern)
	}
	if in.CutoffTZ == "" {
		return fmt.Errorf("%w: cutoff_tz is required", ErrInvalidProvider)
	}
	if _, err := time.LoadLocation(in.CutoffTZ); err != nil {
		return fmt.Errorf("%w: unknown cutoff_tz %q", ErrInvalidProvider, in.CutoffTZ)
	}
	if _, err := parseCutoff(in.CutoffTime); err != nil {
		return err
	}
	if len(in.Currencies) == 0 {
		return fmt.Errorf("%w: at least one currency is required", ErrInvalidProvider)
	}
	for _, currency := range in.Currencies {
		if !currencyPattern.MatchString(currency) {
			return fmt.Errorf("%w: invalid currency %q", ErrInvalidProvider, currency)
		}
	}
	return nil
}

// parseCutoff parses "HH:MM" into the offset from local midnight.
func parseCutoff(value string) (time.Duration, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("%w: cutoff_time must be HH:MM, got %q", ErrInvalidProvider, value)
	}
	return time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute, nil
}

// UpsertProviderTx registers the provider or updates its cut-off. The upsert
// locks the provider row until the caller commits, so concurrent registrations
// of one provider are serialized (and cannot both create its bank accounts).
func (s *Service) UpsertProviderTx(ctx context.Context, tx pgx.Tx, in PutProviderInput) error {
	if err := ValidateProviderInput(in); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO settlement_providers (provider, cutoff_tz, cutoff_time)
		VALUES ($1, $2, $3::time)
		ON CONFLICT (provider) DO UPDATE
		   SET cutoff_tz = EXCLUDED.cutoff_tz,
		       cutoff_time = EXCLUDED.cutoff_time,
		       updated_at = now()`,
		in.Provider, in.CutoffTZ, in.CutoffTime)
	if err != nil {
		return fmt.Errorf("upsert provider: %w", err)
	}
	return nil
}

// EnsureAccountFunc makes sure the provider has a bank account for currency,
// inside the registration transaction. The ledger owns account creation, so
// the caller passes it in.
type EnsureAccountFunc func(ctx context.Context, tx pgx.Tx, provider, currency string) error

// PutProvider registers a provider (or updates its cut-off), makes sure it has
// a bank account per requested currency, and writes an audit entry, all in one
// transaction. It returns the provider as stored.
func (s *Service) PutProvider(ctx context.Context, in PutProviderInput, actor, requestID string, ensure EnsureAccountFunc) (SettlementProvider, error) {
	if err := ValidateProviderInput(in); err != nil {
		return SettlementProvider{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SettlementProvider{}, err
	}
	defer tx.Rollback(ctx)

	if err := s.UpsertProviderTx(ctx, tx, in); err != nil {
		return SettlementProvider{}, err
	}
	for _, currency := range in.Currencies {
		if err := ensure(ctx, tx, in.Provider, currency); err != nil {
			return SettlementProvider{}, fmt.Errorf("provider %s %s account: %w", in.Provider, currency, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log (actor, action, entity_type, entity_id, detail, request_id)
		VALUES ($1, 'settlement_provider.put', 'settlement_provider', $2,
		        jsonb_build_object('cutoff_tz', $3::text, 'cutoff_time', $4::text, 'currencies', $5::text[]), NULLIF($6, ''))`,
		actor, in.Provider, in.CutoffTZ, in.CutoffTime, in.Currencies, requestID); err != nil {
		return SettlementProvider{}, fmt.Errorf("audit provider: %w", err)
	}
	stored, err := getProvider(ctx, tx, in.Provider)
	if err != nil {
		return SettlementProvider{}, err
	}
	return stored, tx.Commit(ctx)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *Service) GetProvider(ctx context.Context, provider string) (SettlementProvider, error) {
	return getProvider(ctx, s.pool, provider)
}

// GetProviderTx reads the provider inside the caller's transaction, so a
// registration can return what it just wrote.
func (s *Service) GetProviderTx(ctx context.Context, tx pgx.Tx, provider string) (SettlementProvider, error) {
	return getProvider(ctx, tx, provider)
}

func getProvider(ctx context.Context, q querier, provider string) (SettlementProvider, error) {
	var p SettlementProvider
	err := q.QueryRow(ctx, `
		SELECT provider, cutoff_tz, to_char(cutoff_time, 'HH24:MI'), created_at, updated_at
		  FROM settlement_providers WHERE provider = $1`, provider,
	).Scan(&p.Provider, &p.CutoffTZ, &p.CutoffTime, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SettlementProvider{}, ErrProviderNotFound
	}
	if err != nil {
		return SettlementProvider{}, err
	}
	p.Accounts, err = providerAccounts(ctx, q, p.Provider)
	return p, err
}

func (s *Service) ListProviders(ctx context.Context) ([]SettlementProvider, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT provider, cutoff_tz, to_char(cutoff_time, 'HH24:MI'), created_at, updated_at
		  FROM settlement_providers ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	providers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SettlementProvider, error) {
		var p SettlementProvider
		err := row.Scan(&p.Provider, &p.CutoffTZ, &p.CutoffTime, &p.CreatedAt, &p.UpdatedAt)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	for i := range providers {
		if providers[i].Accounts, err = providerAccounts(ctx, s.pool, providers[i].Provider); err != nil {
			return nil, err
		}
	}
	return providers, nil
}

func providerAccounts(ctx context.Context, q querier, provider string) ([]ProviderAccount, error) {
	rows, err := q.Query(ctx, `
		SELECT currency, account_id FROM settlement_provider_accounts
		 WHERE provider = $1 ORDER BY currency`, provider)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProviderAccount, error) {
		var a ProviderAccount
		err := row.Scan(&a.Currency, &a.AccountID)
		return a, err
	})
}

// runScope is the set of ledger transfers a run compares a file against.
type runScope struct {
	Start, End time.Time
	// Provider is nil for the legacy scope; Accounts then is nil too and no
	// account filter applies.
	Provider *string
	Accounts []uuid.UUID
}

// windowFor returns the business day that ends on settlementDate at cutoff, in
// loc. A cut-off of 00:00 means the midnight at the end of the date, so it is
// the plain calendar day; any other cut-off ends the day at that local time on
// the date itself (23:00 WIB: from 23:00 the day before up to 23:00).
func windowFor(settlementDate time.Time, loc *time.Location, cutoff time.Duration) (time.Time, time.Time) {
	y, m, d := settlementDate.Date()
	end := time.Date(y, m, d, 0, 0, 0, 0, loc).Add(cutoff)
	if cutoff == 0 {
		end = time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	}
	return end.AddDate(0, 0, -1), end
}

// resolveScope reads the provider configuration in the run's transaction. A
// provider that is not registered keeps the scope used before ADR 0008.
func resolveScope(ctx context.Context, tx pgx.Tx, provider string, settlementDate time.Time) (runScope, error) {
	p, err := getProvider(ctx, tx, provider)
	if errors.Is(err, ErrProviderNotFound) {
		start, end := windowFor(settlementDate, time.UTC, 0)
		return runScope{Start: start, End: end}, nil
	}
	if err != nil {
		return runScope{}, fmt.Errorf("load provider %q: %w", provider, err)
	}
	loc, err := time.LoadLocation(p.CutoffTZ)
	if err != nil {
		return runScope{}, fmt.Errorf("provider %q cutoff_tz: %w", provider, err)
	}
	cutoff, err := parseCutoff(p.CutoffTime)
	if err != nil {
		return runScope{}, err
	}
	start, end := windowFor(settlementDate, loc, cutoff)
	accounts := make([]uuid.UUID, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		accounts = append(accounts, a.AccountID)
	}
	return runScope{Start: start, End: end, Provider: &p.Provider, Accounts: accounts}, nil
}
