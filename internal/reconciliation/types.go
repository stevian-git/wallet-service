package reconciliation

import (
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
	FileContent    []byte
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
