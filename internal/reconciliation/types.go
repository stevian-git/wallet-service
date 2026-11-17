package reconciliation

import (
	"io"
	"time"

	"github.com/google/uuid"
)

type DiscrepancyKind string

const (
	DiscrepancyMissingInLedger       DiscrepancyKind = "missing_in_ledger"
	DiscrepancyMissingInSettlement   DiscrepancyKind = "missing_in_settlement"
	DiscrepancyAmountMismatch        DiscrepancyKind = "amount_mismatch"
	DiscrepancyStatusMismatch        DiscrepancyKind = "status_mismatch"
	DiscrepancyDuplicateInSettlement DiscrepancyKind = "duplicate_in_settlement"
	DiscrepancyDuplicateInLedger     DiscrepancyKind = "duplicate_in_ledger"
	DiscrepancyCurrencyMismatch      DiscrepancyKind = "currency_mismatch"
)

type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
)

type SettlementFile struct {
	ID             uuid.UUID
	Provider       string
	FileName       string
	FileSHA256     []byte
	SettlementDate time.Time
	RecordCount    int
	ImportedAt     time.Time
	// SupersededBy is set once a newer file for the same provider and
	// settlement date is imported; superseded files cannot be reconciled again.
	SupersededBy *uuid.UUID
}

type SettlementRecord struct {
	ID           int64
	FileID       uuid.UUID
	ProviderRef  string
	OurReference *string
	Amount       int64
	Currency     string
	Status       string
	OccurredAt   time.Time
}

type ReconciliationRun struct {
	ID               uuid.UUID
	FileID           uuid.UUID
	Status           RunStatus
	StartedAt        time.Time
	FinishedAt       *time.Time
	MatchedCount     int
	DiscrepancyCount int
	Error            *string
	// Attempts counts claims by a worker; NextAttemptAt is when a run whose
	// worker died becomes claimable again.
	Attempts      int
	NextAttemptAt time.Time
	// The transfer scope the run compared against, set when it completes.
	// ScopeProvider is nil when the file's provider was not registered and the
	// legacy scope (every referenced transfer of the UTC day) was used.
	ScopeStart    *time.Time
	ScopeEnd      *time.Time
	ScopeProvider *string
}

// RetryPolicy bounds how often a run is retried after its worker dies.
type RetryPolicy struct {
	MaxAttempts int
	Base        time.Duration // backoff after the first attempt, doubled per attempt
	Max         time.Duration // backoff cap
}

type Discrepancy struct {
	ID                 int64
	RunID              uuid.UUID
	Kind               DiscrepancyKind
	TransferID         *uuid.UUID
	SettlementRecordID *int64
	ExpectedAmount     *int64
	ActualAmount       *int64
	Resolution         string
	Note               *string
	CreatedAt          time.Time
}

type ImportInput struct {
	Provider       string
	FileName       string
	Content        io.ReadSeeker // read twice: validate+hash, then COPY
	SettlementDate time.Time
}

type CreateRunInput struct {
	FileID uuid.UUID
}

type ListDiscrepanciesInput struct {
	RunID      uuid.UUID
	Resolution *string
	Cursor     string
	Limit      int
}

type DiscrepanciesPage struct {
	Data       []Discrepancy
	NextCursor *string
}

type ListRunsInput struct {
	Cursor string
	Limit  int
}

type RunsPage struct {
	Data       []ReconciliationRun
	NextCursor *string
}

type ListSettlementFilesInput struct {
	Cursor string
	Limit  int
}

type SettlementFilesPage struct {
	Data       []SettlementFile
	NextCursor *string
}

// SettlementProvider is a registered provider: when its business day ends, and
// its bank account per currency (ADR 0008).
type SettlementProvider struct {
	Provider   string
	CutoffTZ   string
	CutoffTime string // "HH:MM", local time in CutoffTZ
	Accounts   []ProviderAccount
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ProviderAccount struct {
	Currency  string
	AccountID uuid.UUID
}

// PutProviderInput registers a provider or updates its cut-off. Currencies are
// only ever added: an account that already has ledger history is never unlinked.
type PutProviderInput struct {
	Provider   string
	CutoffTZ   string
	CutoffTime string
	Currencies []string
}
