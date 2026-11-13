// Package afs defines a read-only agent filesystem. Implementations must authorize
// paths against server-owned mounts; model supplied paths never grant access.
package afs

import (
	"context"
	"errors"
	"path"
	"strings"
)

const MaxReadBytes = 128 << 10

type Entry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}
type FileSystem interface {
	Read(context.Context, string) ([]byte, error)
	List(context.Context, string) ([]Entry, error)
}

func Clean(p string) (string, error) {
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) {
		return "", errors.New("afs: absolute virtual path required")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", errors.New("afs: path traversal denied")
		}
	}
	return path.Clean(p), nil
}
