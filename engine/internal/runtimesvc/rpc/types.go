// Package rpc holds the JSON-RPC 2.0 wire types, the newline-delimited codec,
// the error registry and the method-description registry the protocol schema is
// generated from.
//
// It carries no daemon logic: a client reads the shape from here or from the
// generated schema, and the handlers live elsewhere.
package rpc

import "encoding/json"

// Version is the JSON-RPC version string carried by every message.
const Version = "2.0"

// ProtocolVersion is the daemon protocol version. An additive change bumps the
// minor; a change that removes a field a client can send or read bumps the
// major.
//
// 2.0.0 removed two fields no handler read: Selector.Key and the host envelope
// that every params type used to embed.
//
// 2.1.0 added the groups.code method and GroupsKillParams.ExpectRuns. Both are
// additive, so a client must gate them on the daemon's advertised capabilities
// (CapabilityKillExpectRuns) rather than on this version.
//
// 2.2.0 added the executable facts to the published service row:
// executable_path, executable_modified_at and executable_changed_since_start.
// The change is additive: a client that does not read them is unaffected.
const ProtocolVersion = "2.2.0"

// Request is a client call. ID is raw so a client can use numbers or strings.
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	// ID is echoed back on the reply. Without one the message is the
	// notification shape instead of a call.
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the reply to a Request. Exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	// Result carries the method's result type; Error takes its place when the
	// call failed.
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Notification is a server-initiated message with no reply.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	// Method names what happened; Params is that notification's own payload
	// type, one per name.
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Message is the union the decoder produces: whichever of the three above
// arrived. Callers classify it with IsRequest, IsResponse and IsNotification,
// which share one classifier so the three cannot disagree.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	// Params, Result and Error are whichever of the three shapes the message
	// turned out to be; the classifier below is what decides.
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// messageKind is what a decoded Message turned out to be. A message that is
// neither a call, a notification nor a reply has no kind at all, and none of the
// three predicates answers true for it.
type messageKind int

const (
	kindUnknown messageKind = iota
	kindRequest
	kindNotification
	kindResponse
)

// IsNotification reports whether the message has a method but no id.
func (m Message) IsNotification() bool { return m.kind() == kindNotification }

// IsRequest reports whether the message has both an id and a method.
func (m Message) IsRequest() bool { return m.kind() == kindRequest }

// IsResponse reports whether the message is a reply (id, no method).
func (m Message) IsResponse() bool { return m.kind() == kindResponse }

// kind classifies the message once, so the three predicates cannot drift apart:
// an id together with a method is a call, a method alone is a notification, and
// an id alone is a reply.
func (m Message) kind() messageKind {
	switch {
	case m.Method == "" && m.ID == nil:
		return kindUnknown
	case m.Method == "":
		return kindResponse
	case m.ID == nil:
		return kindNotification
	default:
		return kindRequest
	}
}

// The notification names. Every streaming method replies with a subscription id
// and then pushes stream.chunk messages until stream.end; state.subscribe is the
// exception and keeps its own broadcast names.
const (
	MethodStreamChunk  = "stream.chunk"
	MethodStreamEnd    = "stream.end"
	MethodStreamCancel = "stream.cancel"

	// The state broadcasts: delta and event are what an unscoped subscriber
	// reads, changed is the redacted scoped stream.
	MethodStateDelta   = "state.delta"
	MethodStateEvent   = "state.event"
	MethodStateChanged = "state.changed"
)

// StreamChunk is the payload of a stream.chunk notification.
type StreamChunk struct {
	ID string `json:"id"`
	// Data is one chunk of the method's own chunk type, left undecoded here: a
	// client reads it against the stream_chunk entry of the method it called.
	Data json.RawMessage `json:"data"`
}

// StreamEnd is the payload of a stream.end notification, which closes the
// stream. Data carries the method's final result (ports.wait's
// {ready, timed_out}, for example) and is absent for a stream with nothing to
// say at the end; Error is set when the stream ended because of a failure.
type StreamEnd struct {
	ID    string          `json:"id"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *Error          `json:"error,omitempty"`
}

// StreamCancel is the params of a stream.cancel call: the subscription to stop.
type StreamCancel struct {
	ID string `json:"id"`
}

// StreamStart is what every streaming method answers with immediately: the
// subscription id its chunks will carry.
type StreamStart struct {
	SubscriptionID string `json:"subscription_id"`
}
