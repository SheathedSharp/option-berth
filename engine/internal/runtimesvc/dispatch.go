package runtimesvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// Request is what a Handler receives: the decoded call, the connection it came
// in on, and the daemon runtime it may act through.
type Request struct {
	Runtime *Runtime
	Method  string
	ID      json.RawMessage
	Params  json.RawMessage
	Conn    *Conn
}

// Bind unmarshals the request's params into v. When params are absent, v is
// left at its zero value — so a method whose params are all optional needs no
// special case.
func (r *Request) Bind(v any) error {
	empty := len(r.Params) == 0 || string(r.Params) == "null"
	if empty {
		return nil
	}
	err := json.Unmarshal(r.Params, v)
	if err != nil {
		return rpc.NewError(rpc.CodeInvalidParams,
			fmt.Sprintf("invalid params for %s: %v", r.Method, err), "")
	}
	return nil
}

// Handler serves one method. Returning ErrResponseSent says the handler has
// written its own reply (state.subscribe does, so its snapshot is queued
// atomically with the subscription).
type Handler func(ctx context.Context, req *Request) (any, error)

// ErrResponseSent tells the dispatcher not to write a reply.
var ErrResponseSent = errors.New("daemon: response already sent")

var (
	handlers   = map[string]Handler{}
	handlersMu sync.RWMutex
)

// RegisterHandler adds one method to the dispatcher. A package owning a
// namespace (groups, killer, store, share, sessions) calls it from its own
// init(), so this package never imports that owner. Registering the same method
// twice panics: two owners for a single method is always a bug.
func RegisterHandler(method string, h Handler) {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	if _, taken := handlers[method]; taken {
		panic("daemon: duplicate handler for method " + method)
	}
	handlers[method] = h
}

// lookupHandler returns the handler registered for a method.
func lookupHandler(method string) (Handler, bool) {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	handler, served := handlers[method]
	return handler, served
}

// RegisteredMethods lists every method this build serves, in sorted order.
func RegisteredMethods() []string {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	methods := make([]string, 0, len(handlers))
	for method := range handlers {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return methods
}

// errorFor turns a handler error into a JSON-RPC error object. Anything that is
// not already an *rpc.Error becomes an internal error, so a handler bug never
// leaks a bare Go error string as a protocol code.
func errorFor(err error) *rpc.Error {
	var protocolError *rpc.Error
	if errors.As(err, &protocolError) {
		return protocolError
	}
	return rpc.NewError(rpc.CodeInternal, err.Error(), "")
}
