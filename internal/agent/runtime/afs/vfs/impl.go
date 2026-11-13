// Package vfs adapts an external, authenticated VFS client. This project does not
// depend on ByteDance internal RPCs. Tenant identity travels in the trusted ctx.
package vfs

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
)

type Client interface {
	Read(context.Context, string) ([]byte, error)
	List(context.Context, string) ([]afs.Entry, error)
}
type Authorize func(context.Context, string) error
type FileSystem struct {
	Client    Client
	Authorize Authorize
}

func (f FileSystem) check(ctx context.Context, p string) (string, error) {
	p, e := afs.Clean(p)
	if e != nil {
		return "", e
	}
	if f.Client == nil || f.Authorize == nil {
		return "", errors.New("vfs: authenticated client and authorization required")
	}
	return p, f.Authorize(ctx, p)
}
func (f FileSystem) Read(ctx context.Context, p string) ([]byte, error) {
	p, e := f.check(ctx, p)
	if e != nil {
		return nil, e
	}
	b, e := f.Client.Read(ctx, p)
	if len(b) > afs.MaxReadBytes {
		return nil, errors.New("vfs: file too large")
	}
	return b, e
}
func (f FileSystem) List(ctx context.Context, p string) ([]afs.Entry, error) {
	p, e := f.check(ctx, p)
	if e != nil {
		return nil, e
	}
	return f.Client.List(ctx, p)
}
