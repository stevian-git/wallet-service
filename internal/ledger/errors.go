package ledger

import "errors"

var (
	ErrNotFound          = errors.New("not found")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrAccountFrozen     = errors.New("account frozen")
	ErrCurrencyMismatch  = errors.New("currency mismatch")
	ErrUnbalancedEntries = errors.New("unbalanced entries")
	ErrInvalidAmount     = errors.New("invalid amount")
	ErrInvalidPosting    = errors.New("invalid posting")
	ErrAccountExists     = errors.New("account already exists")
	ErrAlreadyReversed   = errors.New("transfer already reversed")
	ErrNoTransaction     = errors.New("no transaction")
	ErrHoldNotActive     = errors.New("hold is not active")
	ErrHoldExpired       = errors.New("hold expired")
)
