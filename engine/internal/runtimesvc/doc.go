// Package runtimesvc hosts the Runtime Service, Oberth Runtime's local background
// service. It owns RPC admission, observation scheduling and the single-writer
// lifecycle. Worktree/manifest/service/run facts remain authoritative; system
// port discovery is an observation adapter, never a replacement run registry.
// This internal directory/package follows the host responsibility. Public daemon
// commands, RPC names, protocol and disk paths retain their compatible spellings.
package runtimesvc
