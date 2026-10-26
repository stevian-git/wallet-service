package ledger

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type AccountKind string

const (
	AccountKindUserWallet     AccountKind = "user_wallet"
	AccountKindMerchant       AccountKind = "merchant"
	AccountKindSystemBank     AccountKind = "system_bank"
	AccountKindSystemFee      AccountKind = "system_fee"
	AccountKindSystemSuspense AccountKind = "system_suspense"
)

type NormalSide string

const (
	NormalSideDebit  NormalSide = "debit"
	NormalSideCredit NormalSide = "credit"
)

type Direction string

const (
	DirectionDebit  Direction = "debit"
	DirectionCredit Direction = "credit"
)

type TransferType string

const (
	TransferTypeTopup       TransferType = "topup"
	TransferTypeTransfer    TransferType = "transfer"
	TransferTypeWithdrawal  TransferType = "withdrawal"
	TransferTypeHoldCapture TransferType = "hold_capture"
	TransferTypeReversal    TransferType = "reversal"
)

type TransferStatus string

const (
	TransferStatusPending TransferStatus = "pending"
	TransferStatusPosted  TransferStatus = "posted"
	TransferStatusFailed  TransferStatus = "failed"
)

type Account struct {
	ID            uuid.UUID
	Kind          AccountKind
	OwnerRef      *string
	Currency      string
	NormalSide    NormalSide
	AllowNegative bool
	Status        string
	CreatedAt     time.Time
}

type Balance struct {
	AccountID uuid.UUID
	Currency  string
	Posted    int64
	Held      int64
	Available int64
	AsOf      time.Time
}

type Entry struct {
	ID           int64
	TransferID   uuid.UUID
	AccountID    uuid.UUID
	Currency     string
	Direction    Direction
	Amount       int64
	BalanceAfter int64
	CreatedAt    time.Time
}

type Transfer struct {
	ID                   uuid.UUID
	Type                 TransferType
	Status               TransferStatus
	Amount               int64
	Currency             string
	SourceAccountID      *uuid.UUID
	DestinationAccountID *uuid.UUID
	Reference            *string
	ReversesTransferID   *uuid.UUID
	Metadata             json.RawMessage
	CreatedAt            time.Time
	PostedAt             *time.Time
}

type TransferDetail struct {
	Transfer
	Entries []Entry
}

type CreateAccountInput struct {
	OwnerRef string
	Kind     AccountKind
	Currency string
}

type TopupInput struct {
	DestinationAccountID uuid.UUID
	Amount               int64
	Currency             string
	Reference            *string
	Metadata             json.RawMessage
}

type TransferInput struct {
	SourceAccountID      uuid.UUID
	DestinationAccountID uuid.UUID
	Amount               int64
	Currency             string
	Reference            *string
	Metadata             json.RawMessage
}

type WithdrawalInput struct {
	SourceAccountID uuid.UUID
	Amount          int64
	Currency        string
	Reference       *string
	Metadata        json.RawMessage
}

type PostingEntry struct {
	AccountID uuid.UUID
	Currency  string
	Direction Direction
	Amount    int64
}

type PostingRequest struct {
	Type                 TransferType
	SourceAccountID      *uuid.UUID
	DestinationAccountID *uuid.UUID
	Amount               int64
	Currency             string
	Reference            *string
	Metadata             json.RawMessage
	ReversesTransferID   *uuid.UUID
	Entries              []PostingEntry
}
