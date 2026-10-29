package app

import (
	"context"
	"errors"
	"net/http"
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

	parsedDate, err := time.Parse("2006-01-02", settlementDate)
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
		if errors.Is(err, reconciliation.ErrDuplicateFile) {
			return SettlementFileView{}, &duplicateFileError{}
		}
		return SettlementFileView{}, err
	}

	return SettlementFileView{
		ID:             file.ID,
		Provider:       file.Provider,
		FileName:       file.FileName,
		SettlementDate: file.SettlementDate.Format("2006-01-02"),
		RecordCount:    file.RecordCount,
		ImportedAt:     file.ImportedAt,
	}, nil
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
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	page, err := s.recon.ListRuns(ctx, reconciliation.ListRunsInput{
		Cursor: cursor,
		Limit:  limit,
	})
	if err != nil {
		return ReconciliationRunsPageView{}, err
	}

	views := make([]ReconciliationRunView, 0, len(page.Data))
	for _, run := range page.Data {
		views = append(views, reconciliationRunView(run))
	}
	if views == nil {
		views = []ReconciliationRunView{}
	}

	return ReconciliationRunsPageView{
		Data:       views,
		NextCursor: page.NextCursor,
	}, nil
}

func (s *Service) ListSettlementFiles(ctx context.Context, cursor string, limit int) (SettlementFilesPageView, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	page, err := s.recon.ListSettlementFiles(ctx, reconciliation.ListSettlementFilesInput{
		Cursor: cursor,
		Limit:  limit,
	})
	if err != nil {
		return SettlementFilesPageView{}, err
	}

	views := make([]SettlementFileView, 0, len(page.Data))
	for _, file := range page.Data {
		views = append(views, settlementFileView(file))
	}
	if views == nil {
		views = []SettlementFileView{}
	}

	return SettlementFilesPageView{
		Data:       views,
		NextCursor: page.NextCursor,
	}, nil
}

func (s *Service) ListDiscrepancies(ctx context.Context, runID uuid.UUID, resolution *string, cursor string, limit int) (DiscrepanciesPageView, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	page, err := s.recon.ListDiscrepancies(ctx, reconciliation.ListDiscrepanciesInput{
		RunID:      runID,
		Resolution: resolution,
		Cursor:     cursor,
		Limit:      limit,
	})
	if err != nil {
		return DiscrepanciesPageView{}, err
	}

	views := make([]DiscrepancyView, 0, len(page.Data))
	for _, d := range page.Data {
		views = append(views, discrepancyView(d))
	}
	if views == nil {
		views = []DiscrepancyView{}
	}

	return DiscrepanciesPageView{
		Data:       views,
		NextCursor: page.NextCursor,
	}, nil
}

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
		SettlementDate: f.SettlementDate.Format("2006-01-02"),
		RecordCount:    f.RecordCount,
		ImportedAt:     f.ImportedAt,
	}
}

type duplicateFileError struct{}

func (e *duplicateFileError) Error() string {
	return "settlement file with same sha256 already exists"
}

func (e *duplicateFileError) StatusCode() int {
	return http.StatusConflict
}
