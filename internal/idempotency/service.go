package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidKey = errors.New("invalid idempotency key")
	ErrKeyReused  = errors.New("idempotency key reused with a different request")
	ErrInProgress = errors.New("idempotency key is already in progress")
	ErrLeaseLost  = errors.New("idempotency lease was lost")
)

type Service struct {
	pool  *pgxpool.Pool
	lease time.Duration
}

func New(pool *pgxpool.Pool, lease time.Duration) *Service {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	return &Service{pool: pool, lease: lease}
}

type Request struct {
	Scope     string
	Key       string
	Method    string
	Path      string
	RequestID string
	Body      []byte
	Lease     time.Duration
}

type Response struct {
	StatusCode int
	Body       []byte
}

type Outcome struct {
	Response Response
	Replayed bool
}

type Work func(ctx context.Context, tx pgx.Tx) (Response, error)

// ErrorMapper turns a domain error into a stable HTTP response. Returning false
// means the error must not be cached, so the key is released for a retry.
type ErrorMapper func(error) (Response, bool)

func (s *Service) Do(ctx context.Context, req Request, work Work, mapError ErrorMapper) (Outcome, error) {
	if req.Key == "" || len(req.Key) > 255 {
		return Outcome{}, ErrInvalidKey
	}
	if req.Scope == "" {
		req.Scope = "default"
	}

	hash := requestHash(req)
	leaseUntil, replay, err := s.reserve(ctx, req, hash)
	if err != nil {
		return Outcome{}, err
	}
	if replay != nil {
		return *replay, nil
	}

	finished := false
	defer func() {
		if !finished {
			s.release(req, leaseUntil)
		}
	}()

	outcome, err := s.run(ctx, req, leaseUntil, work)
	if err == nil {
		finished = true
		return outcome, nil
	}

	if mapError != nil {
		if response, stable := mapError(err); stable {
			completed, completeErr := s.completeError(ctx, req, leaseUntil, response)
			if completeErr != nil {
				return Outcome{}, completeErr
			}
			finished = true
			return completed, nil
		}
	}

	return Outcome{}, err
}

func (s *Service) reserve(ctx context.Context, req Request, hash []byte) (time.Time, *Outcome, error) {
	for attempt := 0; attempt < 3; attempt++ {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return time.Time{}, nil, err
		}

		leaseUntil := time.Now().UTC().Add(s.leaseFor(req))
		var storedLease time.Time
		err = tx.QueryRow(ctx, `
			INSERT INTO idempotency_keys (scope, key, request_hash, status, locked_until)
			VALUES ($1, $2, $3, 'in_progress', $4)
			ON CONFLICT (scope, key) DO NOTHING
			RETURNING locked_until`,
			req.Scope, req.Key, hash, leaseUntil,
		).Scan(&storedLease)
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return time.Time{}, nil, err
			}
			return storedLease, nil, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			tx.Rollback(ctx)
			return time.Time{}, nil, err
		}

		var (
			existingHash []byte
			status       string
			responseCode pgtype.Int4
			responseBody []byte
			lockedUntil  pgtype.Timestamptz
			expiresAt    time.Time
		)
		err = tx.QueryRow(ctx, `
			SELECT request_hash, status, response_code, response_body, locked_until, expires_at
			  FROM idempotency_keys
			 WHERE scope = $1 AND key = $2
			   FOR UPDATE`,
			req.Scope, req.Key,
		).Scan(&existingHash, &status, &responseCode, &responseBody, &lockedUntil, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			tx.Rollback(ctx)
			continue
		}
		if err != nil {
			tx.Rollback(ctx)
			return time.Time{}, nil, err
		}

		now := time.Now().UTC()
		if !expiresAt.After(now) {
			if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE scope = $1 AND key = $2`, req.Scope, req.Key); err != nil {
				tx.Rollback(ctx)
				return time.Time{}, nil, err
			}
			if err := tx.Commit(ctx); err != nil {
				return time.Time{}, nil, err
			}
			continue
		}

		if !bytes.Equal(existingHash, hash) {
			tx.Rollback(ctx)
			return time.Time{}, nil, ErrKeyReused
		}

		switch status {
		case "completed":
			if !responseCode.Valid || responseBody == nil {
				tx.Rollback(ctx)
				return time.Time{}, nil, fmt.Errorf("idempotency: completed key has no response")
			}
			if err := tx.Commit(ctx); err != nil {
				return time.Time{}, nil, err
			}
			return time.Time{}, &Outcome{
				Response: Response{StatusCode: int(responseCode.Int32), Body: cloneBytes(responseBody)},
				Replayed: true,
			}, nil

		case "in_progress":
			if lockedUntil.Valid && lockedUntil.Time.After(now) {
				tx.Rollback(ctx)
				return time.Time{}, nil, ErrInProgress
			}

			takeoverLease := time.Now().UTC().Add(s.leaseFor(req))
			var stored time.Time
			err := tx.QueryRow(ctx, `
				UPDATE idempotency_keys
				   SET locked_until = $3
				 WHERE scope = $1 AND key = $2
				   AND status = 'in_progress'
				   AND (locked_until IS NULL OR locked_until <= now())
				RETURNING locked_until`,
				req.Scope, req.Key, takeoverLease,
			).Scan(&stored)
			if errors.Is(err, pgx.ErrNoRows) {
				tx.Rollback(ctx)
				return time.Time{}, nil, ErrInProgress
			}
			if err != nil {
				tx.Rollback(ctx)
				return time.Time{}, nil, err
			}
			if err := tx.Commit(ctx); err != nil {
				return time.Time{}, nil, err
			}
			return stored, nil, nil

		default:
			tx.Rollback(ctx)
			return time.Time{}, nil, fmt.Errorf("idempotency: unknown status %q", status)
		}
	}

	return time.Time{}, nil, fmt.Errorf("idempotency: could not reserve key after retries")
}

func (s *Service) run(ctx context.Context, req Request, leaseUntil time.Time, work Work) (Outcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Outcome{}, err
	}
	defer tx.Rollback(ctx)

	response, err := work(ctx, tx)
	if err != nil {
		return Outcome{}, err
	}

	body, err := completeInTx(ctx, tx, req, leaseUntil, response)
	if err != nil {
		return Outcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Outcome{}, err
	}
	return Outcome{Response: Response{StatusCode: response.StatusCode, Body: body}}, nil
}

func (s *Service) completeError(ctx context.Context, req Request, leaseUntil time.Time, response Response) (Outcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Outcome{}, err
	}
	defer tx.Rollback(ctx)

	body, err := completeInTx(ctx, tx, req, leaseUntil, response)
	if err != nil {
		return Outcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Outcome{}, err
	}
	return Outcome{Response: Response{StatusCode: response.StatusCode, Body: body}}, nil
}

func completeInTx(ctx context.Context, tx pgx.Tx, req Request, leaseUntil time.Time, response Response) ([]byte, error) {
	bodyArg := "null"
	if len(response.Body) > 0 {
		bodyArg = string(response.Body)
	}

	var storedBody []byte
	err := tx.QueryRow(ctx, `
		UPDATE idempotency_keys
		   SET status = 'completed',
		       response_code = $3,
		       response_body = $4::jsonb,
		       locked_until = NULL
		 WHERE scope = $1 AND key = $2
		   AND status = 'in_progress'
		   AND locked_until = $5
		RETURNING response_body`,
		req.Scope, req.Key, response.StatusCode, bodyArg, leaseUntil,
	).Scan(&storedBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLeaseLost
	}
	if err != nil {
		return nil, err
	}
	return cloneBytes(storedBody), nil
}

func (s *Service) release(req Request, leaseUntil time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		 WHERE scope = $1 AND key = $2
		   AND status = 'in_progress'
		   AND locked_until = $3`,
		req.Scope, req.Key, leaseUntil)
}

func (s *Service) leaseFor(req Request) time.Duration {
	if req.Lease > 0 {
		return req.Lease
	}
	return s.lease
}

func requestHash(req Request) []byte {
	h := sha256.New()
	h.Write([]byte(req.Method))
	h.Write([]byte{0})
	h.Write([]byte(req.Path))
	h.Write([]byte{0})
	h.Write(req.Body)
	return h.Sum(nil)
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}
