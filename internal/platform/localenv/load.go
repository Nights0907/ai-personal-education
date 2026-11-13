// Package localenv loads the local pilot's private configuration. Existing
// process environment always wins. No interpolation, shell execution or logging.
package localenv

import (
	"bufio"
	"errors"
	"os"
	"strings"
)

func Load(path string) error {
	f, e := os.Open(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	allowed := map[string]bool{"MUSI_MONGO_URI": true, "MUSI_MONGO_DATABASE": true, "MUSI_REDIS_URL": true, "MUSI_OCR_MCP_URL": true, "MUSI_OCR_MCP_TRANSPORT": true, "MUSI_OCR_TOOL_NAME": true, "MUSI_ASSET_DIR": true, "RUNTIME_MYSQL_DSN": true, "BAILIAN_API_KEY": true, "BAILIAN_BASE_URL": true, "BAILIAN_CHAT_MODEL": true}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !allowed[key] {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if e = os.Setenv(key, value); e != nil {
				return e
			}
		}
	}
	return s.Err()
}
