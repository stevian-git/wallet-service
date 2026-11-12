package reconciliation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stevians/wallet-service/internal/testutil"
)

func TestImportCSV(t *testing.T) {
	pg := testutil.NewPostgres(t)
	imp := NewImporter(pg.Pool)
	ctx := context.Background()

	csv := `provider_ref,our_reference,amount,currency,status,occurred_at
prov-001,ref-001,10000,IDR,success,2026-10-26T10:00:00Z
prov-002,ref-002,25000,IDR,success,2026-10-26T11:00:00Z
prov-003,,5000,IDR,success,2026-10-26T12:00:00Z
`

	file, err := imp.Import(ctx, ImportInput{
		Provider:       "mock_bank_a",
		FileName:       "test_settlement.csv",
		FileContent:    []byte(csv),
		SettlementDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if file.Provider != "mock_bank_a" {
		t.Errorf("provider = %q, want %q", file.Provider, "mock_bank_a")
	}
	if file.RecordCount != 3 {
		t.Errorf("record_count = %d, want 3", file.RecordCount)
	}

	var count int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM settlement_records WHERE file_id = $1`, file.ID).Scan(&count); err != nil {
		t.Fatalf("count records: %v", err)
	}
	if count != 3 {
		t.Errorf("settlement_records count = %d, want 3", count)
	}
}

func TestImportDuplicateFile(t *testing.T) {
	pg := testutil.NewPostgres(t)
	imp := NewImporter(pg.Pool)
	ctx := context.Background()

	csv := `provider_ref,our_reference,amount,currency,status,occurred_at
prov-001,ref-001,10000,IDR,success,2026-10-26T10:00:00Z
`

	input := ImportInput{
		Provider:       "mock_bank_a",
		FileName:       "test_settlement.csv",
		FileContent:    []byte(csv),
		SettlementDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
	}

	if _, err := imp.Import(ctx, input); err != nil {
		t.Fatalf("first import: %v", err)
	}

	_, err := imp.Import(ctx, input)
	if err != ErrDuplicateFile {
		t.Errorf("second import error = %v, want ErrDuplicateFile", err)
	}
}

func TestCompareMissingInLedger(t *testing.T) {
	settlements := []SettlementRecord{
		{ID: 1, FileID: uuid.New(), ProviderRef: "prov-1", OurReference: strPtr("ref-1"), Amount: 10000, Currency: "IDR", Status: "success"},
	}
	transfers := []transferRow{}

	discrepancies, matched := compare(settlements, transfers)

	if matched != 0 {
		t.Errorf("matched = %d, want 0", matched)
	}
	if len(discrepancies) != 1 {
		t.Fatalf("discrepancies count = %d, want 1", len(discrepancies))
	}
	if discrepancies[0].Kind != DiscrepancyMissingInLedger {
		t.Errorf("kind = %q, want %q", discrepancies[0].Kind, DiscrepancyMissingInLedger)
	}
}

func TestCompareMissingInSettlement(t *testing.T) {
	settlements := []SettlementRecord{}
	transfers := []transferRow{
		{ID: uuid.New(), Reference: "ref-1", Amount: 10000, Currency: "IDR", Status: "posted"},
	}

	discrepancies, matched := compare(settlements, transfers)

	if matched != 0 {
		t.Errorf("matched = %d, want 0", matched)
	}
	if len(discrepancies) != 1 {
		t.Fatalf("discrepancies count = %d, want 1", len(discrepancies))
	}
	if discrepancies[0].Kind != DiscrepancyMissingInSettlement {
		t.Errorf("kind = %q, want %q", discrepancies[0].Kind, DiscrepancyMissingInSettlement)
	}
}

func TestCompareAmountMismatch(t *testing.T) {
	ref := "ref-1"
	settlements := []SettlementRecord{
		{ID: 1, FileID: uuid.New(), ProviderRef: "prov-1", OurReference: &ref, Amount: 10000, Currency: "IDR", Status: "success"},
	}
	transfers := []transferRow{
		{ID: uuid.New(), Reference: ref, Amount: 12000, Currency: "IDR", Status: "posted"},
	}

	discrepancies, matched := compare(settlements, transfers)

	if matched != 1 {
		t.Errorf("matched = %d, want 1", matched)
	}

	var found bool
	for _, d := range discrepancies {
		if d.Kind == DiscrepancyAmountMismatch {
			found = true
			if d.ExpectedAmount == nil || *d.ExpectedAmount != 12000 {
				t.Errorf("expected_amount = %v, want 12000", d.ExpectedAmount)
			}
			if d.ActualAmount == nil || *d.ActualAmount != 10000 {
				t.Errorf("actual_amount = %v, want 10000", d.ActualAmount)
			}
		}
	}
	if !found {
		t.Error("expected amount_mismatch discrepancy")
	}
}

func TestCompareDuplicateInSettlement(t *testing.T) {
	ref := "ref-1"
	settlements := []SettlementRecord{
		{ID: 1, FileID: uuid.New(), ProviderRef: "prov-1", OurReference: &ref, Amount: 5000, Currency: "IDR", Status: "success"},
		{ID: 2, FileID: uuid.New(), ProviderRef: "prov-2", OurReference: &ref, Amount: 5000, Currency: "IDR", Status: "success"},
	}
	transfers := []transferRow{
		{ID: uuid.New(), Reference: ref, Amount: 10000, Currency: "IDR", Status: "posted"},
	}

	discrepancies, matched := compare(settlements, transfers)

	if matched != 1 {
		t.Errorf("matched = %d, want 1", matched)
	}

	var foundDup bool
	for _, d := range discrepancies {
		if d.Kind == DiscrepancyDuplicateInSettlement {
			foundDup = true
		}
	}
	if !foundDup {
		t.Error("expected duplicate_in_settlement discrepancy")
	}
}

func TestCompareStatusMismatch(t *testing.T) {
	ref := "ref-1"
	settlements := []SettlementRecord{
		{ID: 1, FileID: uuid.New(), ProviderRef: "prov-1", OurReference: &ref, Amount: 10000, Currency: "IDR", Status: "reversed"},
	}
	transfers := []transferRow{
		{ID: uuid.New(), Reference: ref, Amount: 10000, Currency: "IDR", Status: "posted", HasReversal: false},
	}

	discrepancies, matched := compare(settlements, transfers)

	if matched != 1 {
		t.Errorf("matched = %d, want 1", matched)
	}

	var foundStatus bool
	for _, d := range discrepancies {
		if d.Kind == DiscrepancyStatusMismatch {
			foundStatus = true
			if d.Note == nil || !strings.Contains(*d.Note, "has_reversal=false") {
				t.Errorf("note = %v, want contains has_reversal=false", d.Note)
			}
		}
	}
	if !foundStatus {
		t.Error("expected status_mismatch discrepancy")
	}
}

func TestServiceRunsAndPagination(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	ctx := context.Background()

	if _, err := svc.CreateRun(ctx, CreateRunInput{FileID: uuid.New()}); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("create run for unknown file: err = %v, want ErrFileNotFound", err)
	}
	if _, err := svc.GetRun(ctx, uuid.New()); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("get unknown run: err = %v, want ErrRunNotFound", err)
	}

	csv := `provider_ref,our_reference,amount,currency,status,occurred_at
prov-1,ref-a,100,IDR,success,2026-10-26T10:00:00Z
prov-2,ref-b,200,IDR,success,2026-10-26T10:00:00Z
prov-3,ref-c,300,IDR,success,2026-10-26T10:00:00Z
`
	file, err := svc.Import(ctx, ImportInput{
		Provider: "p", FileName: "f.csv", FileContent: []byte(csv),
		SettlementDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	var runIDs []uuid.UUID
	for i := 0; i < 3; i++ {
		run, err := svc.CreateRun(ctx, CreateRunInput{FileID: file.ID})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		if run.Status != RunStatusRunning || run.FileID != file.ID {
			t.Fatalf("created run = %+v", run)
		}
		runIDs = append(runIDs, run.ID)
	}

	first, err := svc.ListRuns(ctx, ListRunsInput{Limit: 2})
	if err != nil || len(first.Data) != 2 || first.NextCursor == nil {
		t.Fatalf("runs page 1: len=%d cursor=%v err=%v", len(first.Data), first.NextCursor, err)
	}
	second, err := svc.ListRuns(ctx, ListRunsInput{Limit: 2, Cursor: *first.NextCursor})
	if err != nil || len(second.Data) != 1 || second.NextCursor != nil {
		t.Fatalf("runs page 2: len=%d cursor=%v err=%v", len(second.Data), second.NextCursor, err)
	}
	if !(first.Data[0].ID.String() > first.Data[1].ID.String() && first.Data[1].ID.String() > second.Data[0].ID.String()) {
		t.Fatal("runs are not ordered by id DESC across pages")
	}

	files, err := svc.ListSettlementFiles(ctx, ListSettlementFilesInput{})
	if err != nil || len(files.Data) != 1 || files.NextCursor != nil || files.Data[0].RecordCount != 3 {
		t.Fatalf("files: %+v err=%v", files, err)
	}

	// With no transfers in scope every record is missing_in_ledger.
	run, err := svc.GetRun(ctx, runIDs[0])
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if err := svc.Reconciler().Execute(ctx, run); err != nil {
		t.Fatalf("execute: %v", err)
	}
	run, err = svc.GetRun(ctx, runIDs[0])
	if err != nil || run.Status != RunStatusCompleted || run.DiscrepancyCount != 3 {
		t.Fatalf("executed run = %+v err=%v", run, err)
	}

	page1, err := svc.ListDiscrepancies(ctx, ListDiscrepanciesInput{RunID: run.ID, Limit: 2})
	if err != nil || len(page1.Data) != 2 || page1.NextCursor == nil {
		t.Fatalf("discrepancies page 1: len=%d cursor=%v err=%v", len(page1.Data), page1.NextCursor, err)
	}
	page2, err := svc.ListDiscrepancies(ctx, ListDiscrepanciesInput{RunID: run.ID, Limit: 2, Cursor: *page1.NextCursor})
	if err != nil || len(page2.Data) != 1 || page2.NextCursor != nil || page2.Data[0].ID <= page1.Data[1].ID {
		t.Fatalf("discrepancies page 2: %+v err=%v", page2, err)
	}

	open, resolved := "open", "resolved"
	if got, err := svc.ListDiscrepancies(ctx, ListDiscrepanciesInput{RunID: run.ID, Resolution: &open}); err != nil || len(got.Data) != 3 {
		t.Fatalf("open filter: len=%d err=%v", len(got.Data), err)
	}
	if got, err := svc.ListDiscrepancies(ctx, ListDiscrepanciesInput{RunID: run.ID, Resolution: &resolved}); err != nil || len(got.Data) != 0 || got.Data == nil {
		t.Fatalf("resolved filter: %+v err=%v", got, err)
	}

	if _, err := svc.ListDiscrepancies(ctx, ListDiscrepanciesInput{RunID: run.ID, Cursor: "!!"}); err == nil || err.Error() != "invalid cursor" {
		t.Fatalf("bad cursor: err = %v", err)
	}
}
