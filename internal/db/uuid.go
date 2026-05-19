package db

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// MustUUID assumes the caller already validated s (e.g. at queue intake)
// and swallows the scan error so call sites stay statement-shaped.
func MustUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(s)
	return u
}

func ParseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return u, fmt.Errorf("parse uuid %q: %w", s, err)
	}
	return u, nil
}

func UUIDString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	v, _ := u.Value()
	s, _ := v.(string)
	return s
}
