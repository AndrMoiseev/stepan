// Package implementationstate defines the durable, provider-neutral state of
// an autonomous implementation run. It deliberately has no dependencies on
// flows, storage, Git, or agent runtimes: controllers and stores use the same
// transition rules without becoming dependencies of one another.
package implementationstate
