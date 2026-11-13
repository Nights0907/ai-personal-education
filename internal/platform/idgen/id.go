// Package idgen issues typed, time-ordered UUIDv7 business identifiers. IDs are
// opaque strings; database positions, not IDs, determine conversation order.
package idgen

import (
	"encoding/hex"
	"github.com/google/uuid"
	"strings"
)

// New deliberately fails closed if the OS random source is unavailable, just
// like uuid.NewString. Random fallbacks and local counters are never used.
func New(kind string) string {
	u := uuid.Must(uuid.NewV7())
	return kind + "_" + hex.EncodeToString(u[:])
}

func Is(kind, id string) bool {
	suffix, ok := strings.CutPrefix(id, kind+"_")
	if !ok || len(suffix) != 32 {
		return false
	}
	u, err := uuid.Parse(suffix)
	return err == nil && u.Version() == 7 && u.Variant() == uuid.RFC4122
}

// Message issues the two typed message identities from one server-issued turn
// number. Replaying the same turn cannot create a second identity. Legacy turn
// IDs deliberately use the previous mapping, preserving all old chain hashes.
func Message(turn, role string) (string, bool) {
	if !Is("t", turn) {
		return "", false
	}
	kind := "ma"
	if role == "user" {
		kind = "mu"
	}
	return kind + turn[1:], true
}
