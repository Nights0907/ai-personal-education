package solve

import (
	"encoding/json"
	"strings"
)

// Preview reads an unfinished tool JSON prefix for display only. It never
// supplies input to validation or persistence. Unknown fields are discarded.
func Preview(raw string) map[string]any {
	if len(raw) > 256*1024 {
		return nil
	}
	p := prefixParser{s: raw}
	root, _ := p.value(0).(map[string]any)
	candidate, _ := root["candidate"].(map[string]any)
	solution, _ := candidate["solution"].(map[string]any)
	out := map[string]any{}
	if v, ok := solution["teacher_response"].(string); ok {
		out["teacher_response"] = v
	}
	if v, ok := solution["analysis"].(string); ok {
		out["analysis"] = v
	}
	for _, field := range []string{"answer", "steps"} {
		items, _ := solution[field].([]any)
		safe := []map[string]string{}
		for _, item := range items {
			m, _ := item.(map[string]any)
			v := map[string]string{}
			for _, key := range []string{"content", "title"} {
				if s, ok := m[key].(string); ok {
					v[key] = s
				}
			}
			if len(v) > 0 {
				safe = append(safe, v)
			}
		}
		if len(safe) > 0 {
			out[field] = safe
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type prefixParser struct {
	s string
	i int
}

func (p *prefixParser) ws() {
	for p.i < len(p.s) && strings.ContainsRune(" \n\r\t", rune(p.s[p.i])) {
		p.i++
	}
}
func (p *prefixParser) str() (string, bool) {
	if p.i >= len(p.s) || p.s[p.i] != '"' {
		return "", false
	}
	start := p.i
	p.i++
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		if c == '\\' {
			if p.i < len(p.s) {
				p.i++
			}
			continue
		}
		if c == '"' {
			var s string
			if json.Unmarshal([]byte(p.s[start:p.i]), &s) != nil {
				return "", false
			}
			return s, true
		}
	}
	end := len(p.s)
	for attempts := 0; end > start && attempts < 12; attempts++ {
		var s string
		if json.Unmarshal([]byte(p.s[start:end]+"\""), &s) == nil {
			return s, false
		}
		end--
	}
	return "", false
}
func (p *prefixParser) value(depth int) any {
	p.ws()
	if p.i >= len(p.s) || depth > 24 {
		return nil
	}
	switch p.s[p.i] {
	case '"':
		v, _ := p.str()
		return v
	case '{':
		p.i++
		out := map[string]any{}
		for {
			p.ws()
			if p.i >= len(p.s) {
				return out
			}
			if p.s[p.i] == '}' {
				p.i++
				return out
			}
			key, complete := p.str()
			if !complete {
				return out
			}
			p.ws()
			if p.i >= len(p.s) || p.s[p.i] != ':' {
				return out
			}
			p.i++
			out[key] = p.value(depth + 1)
			p.ws()
			if p.i >= len(p.s) {
				return out
			}
			if p.s[p.i] == '}' {
				p.i++
				return out
			}
			if p.s[p.i] != ',' {
				return out
			}
			p.i++
		}
	case '[':
		p.i++
		out := []any{}
		for {
			p.ws()
			if p.i >= len(p.s) {
				return out
			}
			if p.s[p.i] == ']' {
				p.i++
				return out
			}
			before := p.i
			out = append(out, p.value(depth+1))
			if p.i == before {
				return out
			}
			p.ws()
			if p.i >= len(p.s) {
				return out
			}
			if p.s[p.i] == ']' {
				p.i++
				return out
			}
			if p.s[p.i] != ',' {
				return out
			}
			p.i++
		}
	default:
		start := p.i
		for p.i < len(p.s) && !strings.ContainsRune(",]} \r\n\t", rune(p.s[p.i])) {
			p.i++
		}
		var v any
		_ = json.Unmarshal([]byte(p.s[start:p.i]), &v)
		return v
	}
}
