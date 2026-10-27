package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/stevians/wallet-service/internal/app"
	"github.com/stevians/wallet-service/internal/problem"
)

const maxBodyBytes = 1 << 20

type Server struct {
	app    *app.Service
	log    *slog.Logger
	apiKey string
	ready  func(context.Context) error
}

func New(service *app.Service, logger *slog.Logger, apiKey string, ready func(context.Context) error) *Server {
	return &Server{app: service, log: logger, apiKey: apiKey, ready: ready}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/accounts", s.handleCreateAccount)
	mux.HandleFunc("GET /v1/accounts/{accountId}", s.handleGetAccount)
	mux.HandleFunc("GET /v1/accounts/{accountId}/balance", s.handleGetBalance)
	mux.HandleFunc("GET /v1/accounts/{accountId}/entries", s.handleListEntries)
	mux.HandleFunc("POST /v1/topups", s.handleTopup)
	mux.HandleFunc("POST /v1/transfers", s.handleTransfer)
	mux.HandleFunc("POST /v1/withdrawals", s.handleWithdrawal)
	mux.HandleFunc("GET /v1/transfers/{transferId}", s.handleGetTransfer)
	mux.HandleFunc("POST /v1/transfers/{transferId}/reversal", s.handleReverseTransfer)
	mux.HandleFunc("POST /v1/holds", s.handleCreateHold)
	mux.HandleFunc("GET /v1/holds/{holdId}", s.handleGetHold)
	mux.HandleFunc("POST /v1/holds/{holdId}/capture", s.handleCaptureHold)
	mux.HandleFunc("POST /v1/holds/{holdId}/release", s.handleReleaseHold)
	mux.HandleFunc("GET /v1/admin/integrity", s.handleIntegrity)
	mux.HandleFunc("POST /v1/reconciliation/settlement-files", s.handleImportSettlementFile)
	mux.HandleFunc("POST /v1/reconciliation/runs", s.handleCreateReconciliationRun)
	mux.HandleFunc("GET /v1/reconciliation/runs/{runId}", s.handleGetReconciliationRun)
	mux.HandleFunc("GET /v1/reconciliation/runs/{runId}/discrepancies", s.handleListDiscrepancies)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("/", s.handleNotFound)

	return s.withRequestID(s.withRecover(s.withLogging(s.withAuth(s.withBodyLimit(mux)))))
}

type requestIDContextKey struct{}

func requestIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey{}).(string)
	return value
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" || len(requestID) > 128 {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID)))
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("panic recovered",
					"panic", recovered,
					"request_id", requestIDFrom(r.Context()),
					"stack", string(debug.Stack()),
				)
				if w.Header().Get("X-Request-ID") == "" {
					w.Header().Set("X-Request-ID", requestIDFrom(r.Context()))
				}
				s.writeProblem(w, r, &problem.Error{
					Status: http.StatusInternalServerError,
					Title:  "Internal server error",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(body)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		s.log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", time.Since(started).Milliseconds(),
			"request_id", requestIDFrom(r.Context()),
		)
	})
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-API-Key")
		if s.apiKey == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.apiKey)) != 1 {
			s.writeProblem(w, r, &problem.Error{
				Status: http.StatusUnauthorized,
				Title:  "Unauthorized",
				Detail: "invalid or missing X-API-Key",
				Slug:   "unauthorized",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/reconciliation/settlement-files" {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.ready(ctx); err != nil {
		s.writeProblem(w, r, &problem.Error{
			Status: http.StatusServiceUnavailable,
			Title:  "Not ready",
			Detail: "database is not reachable",
			Slug:   "not-ready",
		})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.writeProblem(w, r, &problem.Error{
		Status: http.StatusNotFound,
		Code:   "not_found",
		Title:  "Not found",
	})
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	body, err := readRequiredJSON[app.CreateAccountRequest](s, r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	result, err := s.app.CreateAccount(r.Context(), s.meta(r, body.Raw), body.Request)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeResult(w, result)
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathUUID(r, "accountId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	view, err := s.app.GetAccount(r.Context(), accountID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathUUID(r, "accountId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	view, err := s.app.GetBalance(r.Context(), accountID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleListEntries(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathUUID(r, "accountId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 200 {
			s.writeProblem(w, r, validationError("limit must be between 1 and 200"))
			return
		}
		limit = parsed
	}

	page, err := s.app.ListEntries(r.Context(), accountID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleTopup(w http.ResponseWriter, r *http.Request) {
	body, err := readRequiredJSON[app.TopupRequest](s, r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	result, err := s.app.Topup(r.Context(), s.meta(r, body.Raw), body.Request)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeResult(w, result)
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	body, err := readRequiredJSON[app.TransferRequest](s, r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	result, err := s.app.Transfer(r.Context(), s.meta(r, body.Raw), body.Request)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeResult(w, result)
}

func (s *Server) handleWithdrawal(w http.ResponseWriter, r *http.Request) {
	body, err := readRequiredJSON[app.WithdrawalRequest](s, r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	result, err := s.app.Withdrawal(r.Context(), s.meta(r, body.Raw), body.Request)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeResult(w, result)
}

func (s *Server) handleGetTransfer(w http.ResponseWriter, r *http.Request) {
	transferID, err := pathUUID(r, "transferId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	view, err := s.app.GetTransfer(r.Context(), transferID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleReverseTransfer(w http.ResponseWriter, r *http.Request) {
	transferID, err := pathUUID(r, "transferId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}

	raw, err := s.readOptionalBody(r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	body := raw
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte(`{}`)
	}
	var request app.ReversalRequest
	if err := decodeJSON(body, &request); err != nil {
		s.writeProblem(w, r, validationError(err.Error()))
		return
	}

	result, err := s.app.ReverseTransfer(r.Context(), s.meta(r, raw), transferID, request)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeResult(w, result)
}

func (s *Server) handleCreateHold(w http.ResponseWriter, r *http.Request) {
	body, err := readRequiredJSON[app.CreateHoldRequest](s, r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	result, err := s.app.CreateHold(r.Context(), s.meta(r, body.Raw), body.Request)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeResult(w, result)
}

func (s *Server) handleGetHold(w http.ResponseWriter, r *http.Request) {
	holdID, err := pathUUID(r, "holdId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	view, err := s.app.GetHold(r.Context(), holdID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCaptureHold(w http.ResponseWriter, r *http.Request) {
	holdID, err := pathUUID(r, "holdId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	body, err := readRequiredJSON[app.CaptureHoldRequest](s, r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	result, err := s.app.CaptureHold(r.Context(), s.meta(r, body.Raw), holdID, body.Request)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeResult(w, result)
}

func (s *Server) handleReleaseHold(w http.ResponseWriter, r *http.Request) {
	holdID, err := pathUUID(r, "holdId")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	result, err := s.app.ReleaseHold(r.Context(), s.meta(r, nil), holdID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeResult(w, result)
}

func (s *Server) handleIntegrity(w http.ResponseWriter, r *http.Request) {
	view, err := s.app.Integrity(r.Context())
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, view)
}

type decodedRequest[T any] struct {
	Request T
	Raw     []byte
}

func readRequiredJSON[T any](s *Server, r *http.Request) (decodedRequest[T], error) {
	raw, err := s.readBody(r)
	if err != nil {
		return decodedRequest[T]{}, err
	}
	var request T
	if err := decodeJSON(raw, &request); err != nil {
		return decodedRequest[T]{}, validationError(err.Error())
	}
	return decodedRequest[T]{Request: request, Raw: raw}, nil
}

func (s *Server) readBody(r *http.Request) ([]byte, error) {
	body, err := s.readOptionalBody(r)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, validationError("request body is required")
	}
	return body, nil
}

func (s *Server) readOptionalBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &problem.Error{
				Status: http.StatusRequestEntityTooLarge,
				Title:  "Payload too large",
				Detail: "request body exceeds 1 MiB",
				Slug:   "payload-too-large",
			}
		}
		return nil, validationError("could not read request body")
	}
	return body, nil
}

func decodeJSON(body []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	value := r.PathValue(name)
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, validationError(name + " must be a valid UUID")
	}
	return id, nil
}

func validationError(detail string) error {
	return &problem.Error{
		Status: http.StatusUnprocessableEntity,
		Code:   "validation_failed",
		Title:  "Validation failed",
		Detail: detail,
	}
}

func (s *Server) meta(r *http.Request, body []byte) app.Meta {
	return app.Meta{
		Scope:     apiKeyScope(r.Header.Get("X-API-Key")),
		Key:       r.Header.Get("Idempotency-Key"),
		Method:    r.Method,
		Path:      r.URL.Path,
		RequestID: requestIDFrom(r.Context()),
		Body:      body,
	}
}

func apiKeyScope(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		s.writeProblem(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) writeResult(w http.ResponseWriter, result app.Result) {
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	contentType := "application/json"
	if result.StatusCode >= http.StatusBadRequest {
		contentType = "application/problem+json"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(result.StatusCode)
	_, _ = w.Write(result.Body)
}

func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, err error) {
	problemErr := problem.From(err)
	if problemErr.Status >= http.StatusInternalServerError {
		s.log.Error("request failed",
			"error", err,
			"request_id", requestIDFrom(requestContext(r)),
		)
	}
	body := problem.Marshal(problemErr, requestIDFrom(requestContext(r)))
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problemErr.Status)
	_, _ = w.Write(body)
}

func requestContext(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}
