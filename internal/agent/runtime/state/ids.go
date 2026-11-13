package state

import "github.com/musi-ai/education/internal/platform/idgen"

// MessageID is shared by storage, live projection and sealed events. Never
// regenerate a random ID at one of those boundaries.
func MessageID(scope Scope, turn, role string) string {
	if id, ok := idgen.Message(turn, role); ok {
		return id
	}
	return Hash([]string{scope.Key(), turn, role})[:36]
}
