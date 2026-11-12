package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/stevians/wallet-service/internal/reconciliation"
)

func (s *Service) ImportSettlementFile(ctx context.Context, provider, fileName string, content []byte, settlementDate string) (SettlementFileView, error) {
	if provider == "" {
		return SettlementFileView{}, validationError("provider is required")
	}
	if fileName == "" {
		return SettlementFileView{}, validationError("file_name is required")
	}
	if len(content) == 0 {
		return SettlementFileView{}, validationError("file is required")
	}

	parsedDate, err := time.Parse(dateLayout, settlementDate)
	if err != nil {
		return SettlementFileView{}, validationError("settlement_date must be YYYY-MM-DD format")
	}

	file, err := s.recon.Import(ctx, reconciliation.ImportInput{
		Provider:       provider,
		FileName:       fileName,
		FileContent:    content,
		SettlementDate: parsedDate,
	})
	if err != nil {
		return SettlementFileView{}, err
	}
	return settlementFileView(file), nil
}

func (s *Service) CreateReconciliationRun(ctx context.Context, fileID uuid.UUID) (ReconciliationRunView, error) {
	run, err := s.recon.CreateRun(ctx, reconciliation.CreateRunInput{FileID: fileID})
	if err != nil {
		return ReconciliationRunView{}, err
	}
	return reconciliationRunView(run), nil
}

func (s *Service) GetReconciliationRun(ctx context.Context, runID uuid.UUID) (ReconciliationRunView, error) {
	run, err := s.recon.GetRun(ctx, runID)
	if err != nil {
		return ReconciliationRunView{}, err
	}
	return reconciliationRunView(run), nil
}

func (s *Service) ListReconciliationRuns(ctx context.Context, cursor string, limit int) (ReconciliationRunsPageView, error) {
	page, err := s.recon.ListRuns(ctx, reconciliation.ListRunsInput{
		Cursor: cursor,
		Limit:  limit,
	})
	if err != nil {
		return ReconciliationRunsPageView{}, err
	}

	return ReconciliationRunsPageView{
		Data:       mapSlice(page.Data, reconciliationRunView),
		NextCursor: page.NextCursor,
	}, nil
}

func (s *Service) ListSettlementFiles(ctx context.Context, cursor string, limit int) (SettlementFilesPageView, error) {
	page, err := s.recon.ListSettlementFiles(ctx, reconciliation.ListSettlementFilesInput{
		Cursor: cursor,
		Limit:  limit,
	})
	if err != nil {
		return SettlementFilesPageView{}, err
	}

	return SettlementFilesPageView{
		Data:       mapSlice(page.Data, settlementFileView),
		NextCursor: page.NextCursor,
	}, nil
}

func (s *Service) ListDiscrepancies(ctx context.Context, runID uuid.UUID, resolution *string, cursor string, limit int) (DiscrepanciesPageView, error) {
	page, err := s.recon.ListDiscrepancies(ctx, reconciliation.ListDiscrepanciesInput{
		RunID:      runID,
		Resolution: resolution,
		Cursor:     cursor,
		Limit:      limit,
	})
	if err != nil {
		return DiscrepanciesPageView{}, err
	}

	return DiscrepanciesPageView{
		Data:       mapSlice(page.Data, discrepancyView),
		NextCursor: page.NextCursor,
	}, nil
}

// dateLayout is the settlement_date wire format (YYYY-MM-DD).
const dateLayout = "2006-01-02"

func reconciliationRunView(run reconciliation.ReconciliationRun) ReconciliationRunView {
	return ReconciliationRunView{
		ID:               run.ID,
		FileID:           run.FileID,
		Status:           string(run.Status),
		StartedAt:        run.StartedAt,
		FinishedAt:       run.FinishedAt,
		MatchedCount:     run.MatchedCount,
		DiscrepancyCount: run.DiscrepancyCount,
		Error:            run.Error,
	}
}

func discrepancyView(d reconciliation.Discrepancy) DiscrepancyView {
	return DiscrepancyView{
		ID:                 d.ID,
		RunID:              d.RunID,
		Kind:               string(d.Kind),
		TransferID:         d.TransferID,
		SettlementRecordID: d.SettlementRecordID,
		ExpectedAmount:     d.ExpectedAmount,
		ActualAmount:       d.ActualAmount,
		Resolution:         d.Resolution,
		Note:               d.Note,
		CreatedAt:          d.CreatedAt,
	}
}

func settlementFileView(f reconciliation.SettlementFile) SettlementFileView {
	return SettlementFileView{
		ID:             f.ID,
		Provider:       f.Provider,
		FileName:       f.FileName,
		SettlementDate: f.SettlementDate.Format(dateLayout),
		RecordCount:    f.RecordCount,
		ImportedAt:     f.ImportedAt,
	}
}
