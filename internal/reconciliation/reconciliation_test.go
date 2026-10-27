package reconciliation

import (
	"context"
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
