package httpapi

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/stevians/wallet-service/internal/app"
	"github.com/stevians/wallet-service/internal/problem"
)

const (
	maxSettlementFileSize = 256 << 20
	// Multipart parts beyond this many bytes are spooled to a temp file (which
	// net/http removes after the request), so an upload never sits in memory.
	maxSettlementFormMemory = 1 << 20
)

func (s *Server) handleImportSettlementFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSettlementFileSize)

	if err := r.ParseMultipartForm(maxSettlementFormMemory); err != nil {
		s.writeProblem(w, r, &problem.Error{
			Status: http.StatusRequestEntityTooLarge,
			Title:  "Payload too large",
			Detail: "settlement file exceeds 256 MiB",
			Slug:   "payload-too-large",
		})
		return
	}

	provider := r.FormValue("provider")
	settlementDate := r.FormValue("settlement_date")

	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeProblem(w, r, validationError("file is required"))
		return
	}
	defer file.Close()

	view, err := s.app.ImportSettlementFile(r.Context(), provider, header.Filename, file, settlementDate)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	s.writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleCreateReconciliationRun(w http.ResponseWriter, r *http.Request) {
	body, err := readRequiredJSON[app.CreateReconciliationRunRequest](s, r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	if body.Request.FileID == uuid.Nil {
		s.writeProblem(w, r, validationError("file_id is required"))
		return
	}

	result, err := s.app.CreateReconciliationRun(r.Context(), s.meta(r, body.Raw), body.Request.FileID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	s.writeResult(w, result)
}

func (s *Server) handleGetReconciliationRun(w http.ResponseWriter, r *http.Request) {
	runID, err := pathUUID(r, "runId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	view, err := s.app.GetReconciliationRun(r.Context(), runID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	s.writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleListDiscrepancies(w http.ResponseWriter, r *http.Request) {
	runID, err := pathUUID(r, "runId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	limit, err := queryLimit(r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	var resolution *string
	if raw := r.URL.Query().Get("resolution"); raw != "" {
		if raw != "open" && raw != "resolved" && raw != "ignored" {
			s.writeProblem(w, r, validationError("resolution must be open, resolved, or ignored"))
			return
		}
		resolution = &raw
	}

	cursor := r.URL.Query().Get("cursor")

	page, err := s.app.ListDiscrepancies(r.Context(), runID, resolution, cursor, limit)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleListReconciliationRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	cursor := r.URL.Query().Get("cursor")

	page, err := s.app.ListReconciliationRuns(r.Context(), cursor, limit)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleListSettlementFiles(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	cursor := r.URL.Query().Get("cursor")

	page, err := s.app.ListSettlementFiles(r.Context(), cursor, limit)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	s.writeJSON(w, http.StatusOK, page)
}
