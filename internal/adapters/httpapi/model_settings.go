package httpapi

import (
	"encoding/json"
	settings "github.com/musi-ai/education/internal/platform/modelsettings"
	"io"
	"net/http"
	"os"
)

func (s Server) modelSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		settings.Mu.Lock()
		v, e := settings.Read()
		settings.Mu.Unlock()
		if e != nil {
			failure(w, 503, "模型配置读取失败")
			return
		}
		for _, role := range []string{"chat", "memory", "radar"} {
			c, ok := v[role]
			if !ok {
				c = settings.Setting{Provider: "custom", Model: os.Getenv("BAILIAN_CHAT_MODEL"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Configured: os.Getenv("BAILIAN_API_KEY") != ""}
			}
			c.Configured = c.Configured || c.APIKey != ""
			c.APIKey = ""
			v[role] = c
		}
		writeJSON(w, 200, map[string]any{"roles": v, "scope": "本机全局配置", "effective": "下一次模型调用生效"})
		return
	}
	var v map[string]settings.Setting
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF {
		failure(w, 400, "配置格式不正确")
		return
	}
	if e := settings.Save(v); e != nil {
		failure(w, 400, e.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"saved": true})
}
