// Package inmem exposes the local development Agent Store at the runtime seam.
package inmem

import adapter "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"

type Store = adapter.Store

func New() *Store { return adapter.New() }
