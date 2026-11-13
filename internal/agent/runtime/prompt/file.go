// Package prompt reads the editable Markdown system prompt at turn boundaries.
package prompt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const DefaultFile = "agent-design/prompts/teacher-system.md"
const EnvironmentKey = "MUSI_SYSTEM_PROMPT_FILE"
const MaxFileBytes = 128 * 1024

// Resolve pins the file path once at Runtime startup. Only server configuration
// can select it. HTTP query text and model tool arguments never choose this path.
func Resolve(path string) (string, error) {
	if path != "" {
		return filepath.Abs(path)
	}
	if configured := os.Getenv(EnvironmentKey); configured != "" {
		return filepath.Abs(configured)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, filepath.FromSlash(DefaultFile)), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("prompt: project root not found; configure %s or InstructionFile", EnvironmentKey)
}

func Read(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("prompt: read Markdown file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("prompt: system prompt must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > MaxFileBytes {
		return "", errors.New("prompt: Markdown file exceeds 128 KiB")
	}
	if !utf8.Valid(b) {
		return "", errors.New("prompt: Markdown file must be UTF-8")
	}
	text := strings.TrimPrefix(string(b), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if err = Validate(text); err != nil {
		return "", err
	}
	return text, nil
}

// The fixed tag skeleton is checked without treating Markdown/math text as XML.
// For example x < 0 and A & B inside a section remain valid editable Markdown.
func Validate(text string) error {
	if strings.TrimSpace(text) == "" || strings.ContainsRune(text, '\x00') {
		return errors.New("prompt: empty or invalid Markdown")
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "<system-prompt>") || !strings.HasSuffix(trimmed, "</system-prompt>") || strings.Count(text, "<system-prompt>") != 1 || strings.Count(text, "</system-prompt>") != 1 {
		return errors.New("prompt: preserve the system-prompt wrapper")
	}
	position := strings.Index(text, "<system-prompt>") + len("<system-prompt>")
	for _, tag := range []string{"identity", "skill-reminder", "user-memory-reminder"} {
		open, close := "<"+tag+">", "</"+tag+">"
		if strings.Count(text, open) != 1 || strings.Count(text, close) != 1 {
			return fmt.Errorf("prompt: require exactly one %s section", tag)
		}
		start, end := strings.Index(text, open), strings.Index(text, close)
		if start < position || end < start+len(open) || strings.TrimSpace(text[start+len(open):end]) == "" {
			return fmt.Errorf("prompt: empty, nested or unordered %s section", tag)
		}
		position = end + len(close)
	}
	return nil
}
