package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateFileConfinement(t *testing.T) {
	base := t.TempDir()
	f, e := New(filepath.Join(base, "private"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = f.Put("ocr/test.json", []byte(`{"success":true}`)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.Read("ocr/test.json", 2); e == nil {
		t.Fatal("unbounded read")
	}
	os.WriteFile(filepath.Join(base, "outside"), []byte("secret"), 0600)
	if _, e = f.Read("../outside", 100); e == nil {
		t.Fatal("escaped root")
	}
	if e = f.Put("../escape", []byte("bad")); e == nil {
		t.Fatal("escaped write")
	}
}
