//go:build !windows || process_integration

package store

import "sync"

// Shared by isolated filesystem tests and process integration tests.
var stateStoreTestHookMu sync.Mutex
