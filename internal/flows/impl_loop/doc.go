// Package impl_loop contains the autonomous implementation flow.
//
// The controller belongs here. Access to OpenSpec documents belongs to
// internal/openspec; durable run data and check execution live in the nested
// store and checkexec packages. The flow uses shared runtime and infrastructure
// packages outside flows and does not depend on the document-planning flow.
package impl_loop
