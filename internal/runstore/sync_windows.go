//go:build windows

package runstore

// Windows does not support Sync on directory handles. PublishReader syncs the
// completed file before Rename, which is the portable durable-publication
// boundary available here.
func syncDirectory(string) error { return nil }
