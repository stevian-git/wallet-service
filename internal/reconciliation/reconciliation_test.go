package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
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

	// One active run per file, so each run gets its own file (distinct provider).
	var runIDs []uuid.UUID
	for i := 0; i < 3; i++ {
		csv := fmt.Sprintf(`provider_ref,our_reference,amount,currency,status,occurred_at
prov-%[1]d-1,ref-%[1]d-a,100,IDR,success,2026-10-26T10:00:00Z
prov-%[1]d-2,ref-%[1]d-b,200,IDR,success,2026-10-26T10:00:00Z
prov-%[1]d-3,ref-%[1]d-c,300,IDR,success,2026-10-26T10:00:00Z
`, i)
		file, err := svc.Import(ctx, ImportInput{
			Provider: fmt.Sprintf("p%d", i), FileName: "f.csv", FileContent: []byte(csv),
			SettlementDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		run, err := svc.CreateRun(ctx, CreateRunInput{FileID: file.ID})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		if run.Status != RunStatusRunning || run.FileID != file.ID || run.Attempts != 0 {
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
	if err != nil || len(files.Data) != 3 || files.NextCursor != nil || files.Data[0].RecordCount != 3 {
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

// TestExecuteConcurrentWorkersReconcileOnce runs the same run from several
// workers at once. Exactly one may record discrepancies; the rest must back off.
func TestExecuteConcurrentWorkersReconcileOnce(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	ctx := context.Background()

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
	run, err := svc.CreateRun(ctx, CreateRunInput{FileID: file.ID})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	const workers = 4
	errs := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.Reconciler().Execute(ctx, run)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrRunBusy), errors.Is(err, ErrRunNotRunning):
		default:
			t.Fatalf("unexpected execute error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful executions = %d, want 1", succeeded)
	}

	var rows int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_discrepancies WHERE run_id = $1`, run.ID).Scan(&rows); err != nil {
		t.Fatalf("count discrepancies: %v", err)
	}
	got, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if rows != 3 || got.DiscrepancyCount != 3 || got.Status != RunStatusCompleted {
		t.Fatalf("rows=%d run=%+v, want 3 discrepancies on a completed run", rows, got)
	}

	// A finished run is no longer a candidate for any worker.
	next, err := svc.Reconciler().ClaimRun(ctx, testRetry)
	if err != nil || next != nil {
		t.Fatalf("claim after completion = %+v, err = %v; want nil", next, err)
	}
}

func TestCompareIsDeterministic(t *testing.T) {
	var settlements []SettlementRecord
	var transfers []transferRow
	for i := 0; i < 50; i++ {
		ref := fmt.Sprintf("ref-%02d", i)
		settlements = append(settlements, SettlementRecord{
			ID: int64(i + 1), ProviderRef: "p" + ref, OurReference: strPtr(ref),
			Amount: int64(100 + i%3), Currency: "IDR", Status: "success",
		})
		transfers = append(transfers, transferRow{
			ID: uuid.New(), Reference: fmt.Sprintf("ref-%02d", i+25), Amount: 100, Currency: "IDR", Status: "posted",
		})
	}

	want, wantMatched := compare(settlements, transfers)
	for i := 0; i < 20; i++ {
		got, matched := compare(settlements, transfers)
		if matched != wantMatched || !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: compare output differs between identical calls", i)
		}
	}
}

func TestCompareCurrencyMismatch(t *testing.T) {
	ref := "ref-1"
	settlements := []SettlementRecord{
		{ID: 1, ProviderRef: "prov-1", OurReference: &ref, Amount: 10000, Currency: "USD", Status: "success"},
	}
	transfers := []transferRow{
		{ID: uuid.New(), Reference: ref, Amount: 10000, Currency: "IDR", Status: "posted"},
	}

	discrepancies, _ := compare(settlements, transfers)

	if len(discrepancies) != 1 || discrepancies[0].Kind != DiscrepancyCurrencyMismatch {
		t.Fatalf("discrepancies = %+v, want one currency_mismatch", discrepancies)
	}
}

func TestCompareFailedSettlementIsNotCountedAsPaid(t *testing.T) {
	ref := "ref-1"
	settlements := []SettlementRecord{
		{ID: 1, ProviderRef: "prov-1", OurReference: &ref, Amount: 10000, Currency: "IDR", Status: "failed"},
	}
	transfers := []transferRow{
		{ID: uuid.New(), Reference: ref, Amount: 10000, Currency: "IDR", Status: "posted"},
	}

	discrepancies, matched := compare(settlements, transfers)

	if matched != 0 {
		t.Errorf("matched = %d, want 0 (provider reports the payment failed)", matched)
	}
	if len(discrepancies) != 1 || discrepancies[0].Kind != DiscrepancyStatusMismatch {
		t.Fatalf("discrepancies = %+v, want one status_mismatch", discrepancies)
	}
}

var testRetry = RetryPolicy{MaxAttempts: 3, Base: time.Minute, Max: time.Hour}

func importCSV(t *testing.T, svc *Service, provider, date string, rows ...string) SettlementFile {
	t.Helper()
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatalf("parse date: %v", err)
	}
	content := "provider_ref,our_reference,amount,currency,status,occurred_at\n" + strings.Join(rows, "\n") + "\n"
	file, err := svc.Import(context.Background(), ImportInput{
		Provider: provider, FileName: "f.csv", FileContent: []byte(content), SettlementDate: d,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return file
}

func TestImportSupersedesPreviousFile(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	ctx := context.Background()

	original := importCSV(t, svc, "bank_a", "2026-10-26", "prov-1,ref-1,100,IDR,success,2026-10-26T10:00:00Z")
	other := importCSV(t, svc, "bank_b", "2026-10-26", "prov-b1,ref-1,100,IDR,success,2026-10-26T10:00:00Z")
	corrected := importCSV(t, svc, "bank_a", "2026-10-26", "prov-1,ref-1,150,IDR,success,2026-10-26T10:00:00Z")

	files, err := svc.ListSettlementFiles(ctx, ListSettlementFilesInput{})
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	byID := map[uuid.UUID]SettlementFile{}
	for _, f := range files.Data {
		byID[f.ID] = f
	}
	if got := byID[original.ID].SupersededBy; got == nil || *got != corrected.ID {
		t.Fatalf("original superseded_by = %v, want %s", got, corrected.ID)
	}
	if byID[corrected.ID].SupersededBy != nil || byID[other.ID].SupersededBy != nil {
		t.Fatalf("corrected/other file must stay active: %+v / %+v", byID[corrected.ID], byID[other.ID])
	}

	if _, err := svc.CreateRun(ctx, CreateRunInput{FileID: original.ID}); !errors.Is(err, ErrFileSuperseded) {
		t.Fatalf("run on superseded file: err = %v, want ErrFileSuperseded", err)
	}
	if _, err := svc.CreateRun(ctx, CreateRunInput{FileID: corrected.ID}); err != nil {
		t.Fatalf("run on active file: %v", err)
	}
}

func TestConcurrentImportsLeaveOneActiveFile(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	ctx := context.Background()

	const importers = 4
	start := make(chan struct{})
	errs := make(chan error, importers)
	var wg sync.WaitGroup
	for i := 0; i < importers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			content := fmt.Sprintf("provider_ref,our_reference,amount,currency,status,occurred_at\nprov-%d,ref-1,100,IDR,success,2026-10-26T10:00:00Z\n", i)
			_, err := svc.Import(ctx, ImportInput{
				Provider: "bank_a", FileName: "f.csv", FileContent: []byte(content),
				SettlementDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
			})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent import: %v", err)
		}
	}

	var active, total int
	if err := pg.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE superseded_by IS NULL), count(*)
		  FROM settlement_files WHERE provider = 'bank_a'`).Scan(&active, &total); err != nil {
		t.Fatalf("count files: %v", err)
	}
	if active != 1 || total != importers {
		t.Fatalf("active = %d total = %d, want 1 active of %d", active, total, importers)
	}
}

func TestCreateRunAllowsOneActiveRunPerFile(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	ctx := context.Background()
	file := importCSV(t, svc, "bank_a", "2026-10-26", "prov-1,ref-1,100,IDR,success,2026-10-26T10:00:00Z")

	run, err := svc.CreateRun(ctx, CreateRunInput{FileID: file.ID})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := svc.CreateRun(ctx, CreateRunInput{FileID: file.ID}); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("second run while first is running: err = %v, want ErrRunInProgress", err)
	}

	if err := svc.Reconciler().Execute(ctx, run); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := svc.CreateRun(ctx, CreateRunInput{FileID: file.ID}); err != nil {
		t.Fatalf("re-run after completion: %v", err)
	}
}

func TestClaimRunCountsAttemptsAndBacksOff(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	ctx := context.Background()
	file := importCSV(t, svc, "bank_a", "2026-10-26", "prov-1,ref-1,100,IDR,success,2026-10-26T10:00:00Z")
	created, err := svc.CreateRun(ctx, CreateRunInput{FileID: file.ID})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	claimed, err := svc.Reconciler().ClaimRun(ctx, testRetry)
	if err != nil || claimed == nil || claimed.ID != created.ID {
		t.Fatalf("claim = %+v, err = %v; want run %s", claimed, err, created.ID)
	}
	if claimed.Attempts != 1 || !claimed.NextAttemptAt.After(time.Now().Add(testRetry.Base/2)) {
		t.Fatalf("claimed attempts=%d next_attempt_at=%s, want 1 and about now+%s",
			claimed.Attempts, claimed.NextAttemptAt, testRetry.Base)
	}

	// The worker "crashed": the run stays running but is not retried before its backoff.
	again, err := svc.Reconciler().ClaimRun(ctx, testRetry)
	if err != nil || again != nil {
		t.Fatalf("claim during backoff = %+v, err = %v; want nil", again, err)
	}
}

func TestClaimRunFailsRunPastMaxAttempts(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	ctx := context.Background()
	poison := importCSV(t, svc, "bank_a", "2026-10-26", "prov-1,ref-1,100,IDR,success,2026-10-26T10:00:00Z")
	healthy := importCSV(t, svc, "bank_b", "2026-10-26", "prov-b1,ref-1,100,IDR,success,2026-10-26T10:00:00Z")

	poisonRun, err := svc.CreateRun(ctx, CreateRunInput{FileID: poison.ID})
	if err != nil {
		t.Fatalf("create poison run: %v", err)
	}
	healthyRun, err := svc.CreateRun(ctx, CreateRunInput{FileID: healthy.ID})
	if err != nil {
		t.Fatalf("create healthy run: %v", err)
	}
	// The poison run already used every attempt and is due again, ahead of the healthy run.
	if _, err := pg.Pool.Exec(ctx, `
		UPDATE reconciliation_runs SET attempts = $2, next_attempt_at = now() - interval '1 hour'
		 WHERE id = $1`, poisonRun.ID, testRetry.MaxAttempts); err != nil {
		t.Fatalf("exhaust attempts: %v", err)
	}

	claimed, err := svc.Reconciler().ClaimRun(ctx, testRetry)
	if err != nil || claimed == nil || claimed.ID != healthyRun.ID {
		t.Fatalf("claim = %+v, err = %v; want healthy run %s", claimed, err, healthyRun.ID)
	}

	got, err := svc.GetRun(ctx, poisonRun.ID)
	if err != nil {
		t.Fatalf("get poison run: %v", err)
	}
	if got.Status != RunStatusFailed || got.Error == nil || !strings.Contains(*got.Error, "attempts") {
		t.Fatalf("poison run = %+v, want failed with an attempts error", got)
	}
}

func TestCompareDuplicateInLedger(t *testing.T) {
	ref := "ref-1"
	settlements := []SettlementRecord{
		{ID: 1, ProviderRef: "prov-1", OurReference: &ref, Amount: 20000, Currency: "IDR", Status: "success"},
	}
	transfers := []transferRow{
		{ID: uuid.New(), Reference: ref, Amount: 10000, Currency: "IDR", Status: "posted"},
		{ID: uuid.New(), Reference: ref, Amount: 10000, Currency: "IDR", Status: "posted"},
	}

	discrepancies, _ := compare(settlements, transfers)

	if len(discrepancies) != 1 || discrepancies[0].Kind != DiscrepancyDuplicateInLedger {
		t.Fatalf("discrepancies = %+v, want one duplicate_in_ledger", discrepancies)
	}
}
