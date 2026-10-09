//go:build process_integration || nessy_real_cli

package nessyapp

type connectionAuditSnapshot struct {
	Tools                       map[string]int
	PermissionRequests          int
	UniquePermissionRequestIDs  int
	UniquePermissionToolCallIDs int
	AllowOnceSelections         int
	PermissionDenials           int
	PreflightInventory          []string
	PreflightInventoryPresent   bool
}

func (connection *Connection) auditSnapshot() connectionAuditSnapshot {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	tools := make(map[string]int, len(connection.audit.tools))
	for name, count := range connection.audit.tools {
		tools[name] = count
	}
	return connectionAuditSnapshot{
		Tools: tools, PermissionRequests: connection.audit.permissionRequests,
		UniquePermissionRequestIDs:  len(connection.audit.permissionRequestIDs),
		UniquePermissionToolCallIDs: len(connection.audit.permissionToolCallIDs),
		AllowOnceSelections:         connection.audit.allowOnceSelections,
		PermissionDenials:           connection.audit.permissionDenials,
		PreflightInventory:          append([]string(nil), connection.preflightTools...),
		PreflightInventoryPresent:   connection.preflightPresent,
	}
}
