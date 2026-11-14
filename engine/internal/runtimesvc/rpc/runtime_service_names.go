package rpc

import "strings"

// RetiredRuntimeServiceMethodError is a refusal, never a compatibility route.
// Keep the existing JSON-RPC error vocabulary and add an actionable hint.
func RetiredRuntimeServiceMethodError(method string) *Error {
	if !strings.HasPrefix(method, "daemon.") {
		return nil
	}
	return NewError(CodeNotFound,
		"the retired host method namespace was removed in runtime service protocol 3",
		"use a protocol 3 client and runtime.service.*; stop previous hosts explicitly with their own matching CLI before switching")
}
