package app

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/stevians/wallet-service/internal/ledger"
)

type AccountView struct {
	ID        uuid.UUID          `json:"id"`
	Kind      ledger.AccountKind `json:"kind"`
	OwnerRef  string             `json:"owner_ref"`
	Currency  string             `json:"currency"`
	Status    string             `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
}

type BalanceView struct {
	AccountID uuid.UUID `json:"account_id"`
	Currency  string    `json:"currency"`
	Posted    int64     `json:"posted"`
	Held      int64     `json:"held"`
	Available int64     `json:"available"`
	AsOf      time.Time `json:"as_of"`
}

type EntryView struct {
	ID           int64            `json:"id"`
	TransferID   uuid.UUID        `json:"transfer_id"`
	AccountID    uuid.UUID        `json:"account_id"`
	Direction    ledger.Direction `json:"direction"`
	Amount       int64            `json:"amount"`
	Currency     string           `json:"currency"`
	BalanceAfter int64            `json:"balance_after"`
	CreatedAt    time.Time        `json:"created_at"`
}

type EntriesPage struct {
	Data       []EntryView `json:"data"`
	NextCursor *string     `json:"next_cursor"`
}

type TransferView struct {
	ID                   uuid.UUID             `json:"id"`
	Type                 ledger.TransferType   `json:"type"`
	Status               ledger.TransferStatus `json:"status"`
	Amount               int64                 `json:"amount"`
	Currency             string                `json:"currency"`
	SourceAccountID      *uuid.UUID            `json:"source_account_id"`
	DestinationAccountID *uuid.UUID            `json:"destination_account_id"`
	Reference            *string               `json:"reference"`
	ReversesTransferID   *uuid.UUID            `json:"reverses_transfer_id"`
	Metadata             json.RawMessage       `json:"metadata"`
	CreatedAt            time.Time             `json:"created_at"`
	PostedAt             *time.Time            `json:"posted_at"`
}

type TransferDetailView struct {
	TransferView
	Entries []EntryView `json:"entries"`
}

type BalanceDriftView struct {
	AccountID     uuid.UUID `json:"account_id"`
	CachedPosted  int64     `json:"cached_posted"`
	DerivedPosted int64     `json:"derived_posted"`
}

type UnbalancedTransferView struct {
	TransferID  uuid.UUID `json:"transfer_id"`
	TotalDebit  int64     `json:"total_debit"`
	TotalCredit int64     `json:"total_credit"`
}

type IntegrityView struct {
	OK                   bool                     `json:"ok"`
	CheckedAt            time.Time                `json:"checked_at"`
	BalanceDrift         []BalanceDriftView       `json:"balance_drift"`
	UnbalancedTransfers  []UnbalancedTransferView `json:"unbalanced_transfers"`
	PostedWithoutEntries []uuid.UUID              `json:"posted_without_entries"`
}

func accountView(account ledger.Account) AccountView {
	ownerRef := ""
	if account.OwnerRef != nil {
		ownerRef = *account.OwnerRef
	}
	return AccountView{
		ID:        account.ID,
		Kind:      account.Kind,
		OwnerRef:  ownerRef,
		Currency:  account.Currency,
		Status:    account.Status,
		CreatedAt: account.CreatedAt,
	}
}

func balanceView(balance ledger.Balance) BalanceView {
	return BalanceView{
		AccountID: balance.AccountID,
		Currency:  balance.Currency,
		Posted:    balance.Posted,
		Held:      balance.Held,
		Available: balance.Available,
		AsOf:      balance.AsOf,
	}
}

func entryView(entry ledger.Entry) EntryView {
	return EntryView{
		ID:           entry.ID,
		TransferID:   entry.TransferID,
		AccountID:    entry.AccountID,
		Direction:    entry.Direction,
		Amount:       entry.Amount,
		Currency:     entry.Currency,
		BalanceAfter: entry.BalanceAfter,
		CreatedAt:    entry.CreatedAt,
	}
}

func entryViews(entries []ledger.Entry) []EntryView {
	views := make([]EntryView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, entryView(entry))
	}
	return views
}

func transferView(transfer ledger.Transfer) TransferView {
	metadata := transfer.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	return TransferView{
		ID:                   transfer.ID,
		Type:                 transfer.Type,
		Status:               transfer.Status,
		Amount:               transfer.Amount,
		Currency:             transfer.Currency,
		SourceAccountID:      transfer.SourceAccountID,
		DestinationAccountID: transfer.DestinationAccountID,
		Reference:            transfer.Reference,
		ReversesTransferID:   transfer.ReversesTransferID,
		Metadata:             metadata,
		CreatedAt:            transfer.CreatedAt,
		PostedAt:             transfer.PostedAt,
	}
}

func transferDetailView(detail ledger.TransferDetail) TransferDetailView {
	return TransferDetailView{
		TransferView: transferView(detail.Transfer),
		Entries:      entryViews(detail.Entries),
	}
}

func integrityView(report ledger.IntegrityReport) IntegrityView {
	drift := make([]BalanceDriftView, 0, len(report.BalanceDrift))
	for _, item := range report.BalanceDrift {
		drift = append(drift, BalanceDriftView{
			AccountID:     item.AccountID,
			CachedPosted:  item.CachedPosted,
			DerivedPosted: item.DerivedPosted,
		})
	}

	unbalanced := make([]UnbalancedTransferView, 0, len(report.UnbalancedTransfers))
	for _, item := range report.UnbalancedTransfers {
		unbalanced = append(unbalanced, UnbalancedTransferView{
			TransferID:  item.TransferID,
			TotalDebit:  item.TotalDebit,
			TotalCredit: item.TotalCredit,
		})
	}

	missing := append([]uuid.UUID(nil), report.PostedWithoutEntries...)
	if missing == nil {
		missing = []uuid.UUID{}
	}

	return IntegrityView{
		OK:                   len(drift) == 0 && len(unbalanced) == 0 && len(missing) == 0,
		CheckedAt:            report.CheckedAt,
		BalanceDrift:         drift,
		UnbalancedTransfers:  unbalanced,
		PostedWithoutEntries: missing,
	}
}

func encodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func decodeCursor(cursor string) (*int64, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, validationError("invalid cursor")
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || id <= 0 {
		return nil, validationError("invalid cursor")
	}
	return &id, nil
}
