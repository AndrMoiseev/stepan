// Package impl_loop contains the autonomous implementation flow.
//
// The controller belongs here. Access to OpenSpec documents, durable run data,
// and execution of checks belong to the internal openspec, runstore, and
// checkexec components respectively. The flow uses shared runtime and
// infrastructure packages outside flows and does not depend on the
// document-planning flow.
package impl_loop
