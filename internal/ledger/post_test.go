package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPostRejectsUnbalancedEntriesBeforeDB(t *testing.T) {
	ledger := New(nil)
	from := uuid.New()
	to := uuid.New()

	_, err := ledger.Post(context.Background(), nil, PostingRequest{
		Type:                 TransferTypeTransfer,
		SourceAccountID:      &from,
		DestinationAccountID: &to,
		Amount:               100,
		Currency:             "IDR",
		Entries: []PostingEntry{
			{AccountID: from, Currency: "IDR", Direction: DirectionDebit, Amount: 100},
			{AccountID: to, Currency: "IDR", Direction: DirectionCredit, Amount: 90},
		},
	})

	if !errors.Is(err, ErrUnbalancedEntries) {
		t.Fatalf("expected ErrUnbalancedEntries, got %v", err)
	}
}

func TestPostRejectsMixedCurrenciesBeforeDB(t *testing.T) {
	ledger := New(nil)
	from := uuid.New()
	to := uuid.New()

	_, err := ledger.Post(context.Background(), nil, PostingRequest{
		Type:                 TransferTypeTransfer,
		SourceAccountID:      &from,
		DestinationAccountID: &to,
		Amount:               100,
		Currency:             "IDR",
		Entries: []PostingEntry{
			{AccountID: from, Currency: "IDR", Direction: DirectionDebit, Amount: 100},
			{AccountID: to, Currency: "USD", Direction: DirectionCredit, Amount: 100},
		},
	})

	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected ErrCurrencyMismatch, got %v", err)
	}
}

func TestPostRejectsNonPositiveAmountsBeforeDB(t *testing.T) {
	ledger := New(nil)
	from := uuid.New()
	to := uuid.New()

	tests := []struct {
		name string
		req  PostingRequest
	}{
		{
			name: "zero transfer amount",
			req: PostingRequest{
				Type:                 TransferTypeTransfer,
				SourceAccountID:      &from,
				DestinationAccountID: &to,
				Amount:               0,
				Currency:             "IDR",
				Entries: []PostingEntry{
					{AccountID: from, Currency: "IDR", Direction: DirectionDebit, Amount: 10},
					{AccountID: to, Currency: "IDR", Direction: DirectionCredit, Amount: 10},
				},
			},
		},
		{
			name: "negative entry amount",
			req: PostingRequest{
				Type:                 TransferTypeTransfer,
				SourceAccountID:      &from,
				DestinationAccountID: &to,
				Amount:               10,
				Currency:             "IDR",
				Entries: []PostingEntry{
					{AccountID: from, Currency: "IDR", Direction: DirectionDebit, Amount: -10},
					{AccountID: to, Currency: "IDR", Direction: DirectionCredit, Amount: -10},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ledger.Post(context.Background(), nil, tt.req); !errors.Is(err, ErrInvalidAmount) {
				t.Fatalf("expected ErrInvalidAmount, got %v", err)
			}
		})
	}
}

func TestPostRejectsDuplicateAccountsBeforeDB(t *testing.T) {
	ledger := New(nil)
	account := uuid.New()

	_, err := ledger.Post(context.Background(), nil, PostingRequest{
		Type:      TransferTypeTransfer,
		Amount:    100,
		Currency:  "IDR",
		Reference: nil,
		Entries: []PostingEntry{
			{AccountID: account, Currency: "IDR", Direction: DirectionDebit, Amount: 100},
			{AccountID: account, Currency: "IDR", Direction: DirectionCredit, Amount: 100},
		},
	})

	if !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("expected ErrInvalidPosting, got %v", err)
	}
}

func TestPostRequiresTransactionAfterValidation(t *testing.T) {
	ledger := New(nil)
	from := uuid.New()
	to := uuid.New()

	_, err := ledger.Post(context.Background(), nil, PostingRequest{
		Type:                 TransferTypeTransfer,
		SourceAccountID:      &from,
		DestinationAccountID: &to,
		Amount:               100,
		Currency:             "IDR",
		Entries: []PostingEntry{
			{AccountID: from, Currency: "IDR", Direction: DirectionDebit, Amount: 100},
			{AccountID: to, Currency: "IDR", Direction: DirectionCredit, Amount: 100},
		},
	})

	if !errors.Is(err, ErrNoTransaction) {
		t.Fatalf("expected ErrNoTransaction, got %v", err)
	}
}
