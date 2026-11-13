package agentstore

import (
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
	"testing"
)

func TestStoreContract(t *testing.T) {
	contracttest.Run(t, func(*testing.T) storage.Store { return New() })
}
