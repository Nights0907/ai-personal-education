package modelsettings

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Setting struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key,omitempty"`
	Configured bool   `json:"configured"`
}

var Mu sync.Mutex
var Providers = map[string]string{"bailian": "https://dashscope.aliyuncs.com/compatible-mode/v1", "deepseek": "https://api.deepseek.com/v1", "openai": "https://api.openai.com/v1", "custom": ""}

const File = "var/private-config/models.json"

func Read() (map[string]Setting, error) {
	b, e := os.ReadFile(File)
	if os.IsNotExist(e) {
		return map[string]Setting{}, nil
	}
	if e != nil {
		return nil, e
	}
	var v map[string]Setting
	e = json.Unmarshal(b, &v)
	return v, e
}
func Save(v map[string]Setting) error {
	Mu.Lock()
	defer Mu.Unlock()
	old, e := Read()
	if e != nil {
		return e
	}
	for role, c := range v {
		if role != "chat" && role != "memory" && role != "radar" {
			return errors.New("未知模型用途")
		}
		base, ok := Providers[c.Provider]
		if !ok {
			return errors.New("未知厂商")
		}
		if base != "" {
			c.BaseURL = base
		}
		u, e := url.Parse(c.BaseURL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" {
			return errors.New("Base URL格式不正确")
		}
		if strings.TrimSpace(c.Model) == "" || len(c.Model) > 160 {
			return errors.New("请输入模型名称")
		}
		if c.APIKey == "" {
			c.APIKey = old[role].APIKey
			if c.APIKey == "" && c.BaseURL == os.Getenv("BAILIAN_BASE_URL") {
				c.APIKey = os.Getenv("BAILIAN_API_KEY")
			}
		}
		if c.APIKey == "" {
			return errors.New("请输入API Key")
		}
		c.Configured = true
		old[role] = c
	}
	if e = os.MkdirAll(filepath.Dir(File), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(old)
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(File), "models-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	tmp.Chmod(0600)
	if _, e = tmp.Write(b); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	return os.Rename(name, File)
}
