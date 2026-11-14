// Package daemon hosts the Runtime Service, Oberth Runtime's local background
// service. It owns RPC admission, observation scheduling and the single-writer
// lifecycle. Worktree/manifest/service/run facts remain authoritative; system
// port discovery is an observation adapter, never a replacement run registry.
// The package name, daemon commands, protocol and disk paths are compatibility
// interfaces; the responsibility name does not rename or migrate them.
package daemon
