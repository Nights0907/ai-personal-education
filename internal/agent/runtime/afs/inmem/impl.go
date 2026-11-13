package inmem

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	"sort"
	"strings"
)

type FileSystem struct{ files map[string][]byte }

func New(files map[string][]byte) (*FileSystem, error) {
	f := &FileSystem{files: map[string][]byte{}}
	for p, b := range files {
		n, e := afs.Clean(p)
		if e != nil {
			return nil, e
		}
		if len(b) > afs.MaxReadBytes {
			return nil, errors.New("afs: file too large")
		}
		f.files[n] = append([]byte(nil), b...)
	}
	return f, nil
}
func (f *FileSystem) Read(ctx context.Context, p string) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	p, e := afs.Clean(p)
	if e != nil {
		return nil, e
	}
	b, ok := f.files[p]
	if !ok {
		return nil, errors.New("afs: file not found or not authorized")
	}
	return append([]byte(nil), b...), nil
}
func (f *FileSystem) List(ctx context.Context, p string) ([]afs.Entry, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	p, e := afs.Clean(p)
	if e != nil {
		return nil, e
	}
	prefix := strings.TrimSuffix(p, "/") + "/"
	found := map[string]afs.Entry{}
	for name := range f.files {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		tail := strings.TrimPrefix(name, prefix)
		parts := strings.SplitN(tail, "/", 2)
		key := prefix + parts[0]
		found[key] = afs.Entry{Path: key, IsDir: len(parts) > 1}
	}
	out := []afs.Entry{}
	for _, v := range found {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
