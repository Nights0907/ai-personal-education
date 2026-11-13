package local

import (
	"errors"
	"github.com/musi-ai/education/internal/platform/idgen"
	"io"
	"os"
	"path/filepath"
)

// Root operations reject traversal and escaping symlinks. Keys are opaque,
// service-created references, never paths supplied to a model tool.
type Files struct{ root *os.Root }

func New(path string) (*Files, error) {
	if e := os.MkdirAll(path, 0700); e != nil {
		return nil, e
	}
	r, e := os.OpenRoot(path)
	if e != nil {
		return nil, e
	}
	return &Files{root: r}, nil
}
func (f *Files) Close() error { return f.root.Close() }
func (f *Files) Read(key string, limit int) ([]byte, error) {
	r, e := f.root.Open(key)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if e == nil && len(b) > limit {
		e = errors.New("asset exceeds read limit")
	}
	return b, e
}
func (f *Files) Put(key string, data []byte) error {
	if e := f.root.MkdirAll(filepath.Dir(key), 0700); e != nil {
		return e
	}
	tmp := key + "." + idgen.New("tmp")
	w, e := f.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.root.Remove(tmp)
	if _, e = w.Write(data); e != nil {
		w.Close()
		return e
	}
	if e = w.Close(); e != nil {
		return e
	}
	if e = f.root.Rename(tmp, key); e != nil {
		existing, readErr := f.Read(key, len(data))
		if readErr == nil && string(existing) == string(data) {
			return nil
		}
		return e
	}
	return nil
}
func (f *Files) Delete(key string) error { return f.root.Remove(key) }
