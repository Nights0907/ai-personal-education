package runtime

import "context"

type sessionKey string

const (
	PreambleKey sessionKey = "runtime.preamble"
	MemoryKey   sessionKey = "runtime.memory"
)

func SessionValue(ctx context.Context, key string) (string, bool) {
	s, ok := ctx.Value(sessionKey(key)).(string)
	return s, ok
}
