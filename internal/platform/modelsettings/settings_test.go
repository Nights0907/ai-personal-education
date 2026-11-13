package modelsettings

import (
	"os"
	"testing"
)

func TestConfigPersistsAndPreservesSecret(t *testing.T) {
	t.Chdir(t.TempDir())
	if e := Save(map[string]Setting{"chat": {Provider: "deepseek", Model: "deepseek-chat", APIKey: "test-secret"}}); e != nil {
		t.Fatal(e)
	}
	v, e := Read()
	if e != nil || v["chat"].BaseURL != Providers["deepseek"] {
		t.Fatal(v, e)
	}
	if e = Save(map[string]Setting{"chat": {Provider: "deepseek", Model: "deepseek-chat"}}); e != nil {
		t.Fatal(e)
	}
	v, _ = Read()
	if v["chat"].APIKey != "test-secret" {
		t.Fatal("secret lost")
	}
	if _, e = os.Stat(File); e != nil {
		t.Fatal(e)
	}
	if e = Save(map[string]Setting{"unknown": {Provider: "custom"}}); e == nil {
		t.Fatal("unknown role accepted")
	}
	if e = Save(map[string]Setting{"memory": {Provider: "custom", Model: "x", BaseURL: "file:///tmp", APIKey: "x"}}); e == nil {
		t.Fatal("invalid endpoint accepted")
	}
}
