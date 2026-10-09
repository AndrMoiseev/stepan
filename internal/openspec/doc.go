// Package openspec isolates access to an OpenSpec change's documents and
// document versions.
//
// It deliberately does not define a generic specification-adapter interface or
// parse Markdown tasks. Agents extract task structure and select relevant
// documents when the implementation flow is added.
package openspec
