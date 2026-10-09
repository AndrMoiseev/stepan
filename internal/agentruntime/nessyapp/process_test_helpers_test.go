//go:build process_integration

package nessyapp

func (connection *Connection) call(method string, params, result any) error {
	return connection.callAndCommit(method, params, result, nil)
}

func newProcess(config Config, artifactRoot string, deps processDependencies) *Process {
	return newProcessWithWorkspaceWrite(config, artifactRoot, false, deps)
}
