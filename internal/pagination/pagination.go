// Package pagination holds the keyset-pagination helpers shared by the ledger,
// reconciliation, and app layers so limits and cursor formats stay identical.
package pagination

import (
	"encoding/base64"
	"errors"
	"strconv"

	"github.com/google/uuid"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

var ErrInvalidCursor = errors.New("invalid cursor")

// ClampLimit maps non-positive limits to DefaultLimit and caps at MaxLimit.
func ClampLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}

// Trim cuts a limit+1 result set down to limit and reports whether more rows
// exist. Callers fetch limit+1 rows so the extra row signals a next page.
func Trim[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

func EncodeInt64(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func DecodeInt64(cursor string) (*int64, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || id <= 0 {
		return nil, ErrInvalidCursor
	}
	return &id, nil
}

func EncodeUUID(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String()))
}

func DecodeUUID(cursor string) (*uuid.UUID, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(string(raw))
	if err != nil {
		return nil, ErrInvalidCursor
	}
	return &id, nil
}
