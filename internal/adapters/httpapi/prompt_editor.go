package httpapi

import (
	"encoding/json"
	"github.com/musi-ai/education/internal/agent/runtime/prompt"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

var promptEditMu sync.Mutex

func (s Server) editablePrompt(w http.ResponseWriter, r *http.Request) {
	promptEditMu.Lock()
	defer promptEditMu.Unlock()
	path, err := prompt.Resolve("")
	if err != nil {
		failure(w, 503, "提示词路径不可用")
		return
	}
	content, err := prompt.Read(path)
	if err != nil {
		failure(w, 503, "提示词读取失败")
		return
	}
	if r.Method == "GET" {
		writeJSON(w, 200, map[string]string{"content": content, "revision": state.Hash(content), "scope": "shared", "effect": "next_request"})
		return
	}
	var body struct {
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 160<<10))
	d.DisallowUnknownFields()
	var tail any
	if d.Decode(&body) != nil || d.Decode(&tail) != io.EOF {
		failure(w, 400, "编辑格式不正确")
		return
	}
	if body.Revision != state.Hash(content) {
		failure(w, 409, "提示词已变更，请重新加载后再编辑")
		return
	}
	if len(body.Content) > prompt.MaxFileBytes || prompt.Validate(body.Content) != nil {
		failure(w, 400, "请保留system-prompt、identity、skill-reminder、user-memory-reminder完整结构")
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".prompt-edit-*")
	if err != nil {
		failure(w, 503, "无法保存提示词")
		return
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err = file.WriteString(body.Content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp, path)
	}
	if err != nil {
		failure(w, 503, "保存失败，原提示词保留")
		return
	}
	writeJSON(w, 200, map[string]string{"revision": state.Hash(body.Content), "effect": "next_request"})
}
