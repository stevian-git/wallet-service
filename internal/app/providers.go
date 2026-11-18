package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/stevians/wallet-service/internal/reconciliation"
)

type ProviderAccountView struct {
	Currency  string    `json:"currency"`
	AccountID uuid.UUID `json:"account_id"`
}

type SettlementProviderView struct {
	Provider   string                `json:"provider"`
	CutoffTZ   string                `json:"cutoff_tz"`
	CutoffTime string                `json:"cutoff_time"`
	Accounts   []ProviderAccountView `json:"accounts"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
}

type SettlementProvidersView struct {
	Data []SettlementProviderView `json:"data"`
}

// PutSettlementProvider registers a settlement provider or updates its cut-off
// (ADR 0008). PUT is idempotent by itself: repeating it changes nothing, and
// currencies are only added, so it needs no Idempotency-Key.
func (s *Service) PutSettlementProvider(ctx context.Context, meta Meta, provider string, req PutSettlementProviderRequest) (SettlementProviderView, error) {
	stored, err := s.recon.PutProvider(ctx, reconciliation.PutProviderInput{
		Provider:   provider,
		CutoffTZ:   req.CutoffTZ,
		CutoffTime: req.CutoffTime,
		Currencies: req.Currencies,
	}, meta.Scope, meta.RequestID, func(ctx context.Context, tx pgx.Tx, provider, currency string) error {
		_, _, err := s.book.EnsureProviderBankAccount(ctx, tx, provider, currency)
		return err
	})
	if err != nil {
		return SettlementProviderView{}, err
	}
	return settlementProviderView(stored), nil
}

func (s *Service) ListSettlementProviders(ctx context.Context) (SettlementProvidersView, error) {
	providers, err := s.recon.ListProviders(ctx)
	if err != nil {
		return SettlementProvidersView{}, err
	}
	return SettlementProvidersView{Data: mapSlice(providers, settlementProviderView)}, nil
}

func settlementProviderView(p reconciliation.SettlementProvider) SettlementProviderView {
	return SettlementProviderView{
		Provider:   p.Provider,
		CutoffTZ:   p.CutoffTZ,
		CutoffTime: p.CutoffTime,
		Accounts: mapSlice(p.Accounts, func(a reconciliation.ProviderAccount) ProviderAccountView {
			return ProviderAccountView{Currency: a.Currency, AccountID: a.AccountID}
		}),
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}
