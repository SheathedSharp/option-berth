package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/invopop/jsonschema"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The protocol schema is generated, never hand-edited.
//
//go:generate go run ../../../cmd/gen-schema -o ../../../docs/schema/protocol.schema.json

// Description is one method's wire contract.
//
// Chunk and End are set only for streaming methods: those reply immediately with
// a subscription id, push stream.chunk notifications carrying Chunk, and finish
// with a stream.end carrying End.
type Description struct {
	Method string // the JSON-RPC method name a client sends
	Params any    // the request params type, reflected into the document
	Result any    // the result type the matching response carries
	Chunk  any    // per-notification payload, streaming methods only
	End    any    // stream.end payload, streaming methods only
}

// NotificationDescription is the wire contract of one server-pushed message.
// A notification has no id and gets no reply, so its params are all there is to
// describe.
type NotificationDescription struct {
	Method string
	Params any
}

// The registries are filled by Describe and DescribeNotification from the init
// functions below, and read by BuildSchema.
var (
	registry             = map[string]Description{}
	notificationRegistry = map[string]NotificationDescription{}
)

// Describe registers a method's wire contract. The packages that own a
// namespace call it from their own init().
func Describe(method string, params, result, chunk, end any) {
	registry[method] = Description{ // keyed by method: a later registration of
		// the same name replaces the earlier one, which is how an owning package
		// can correct a definition the core set declared
		Method: method,
		Params: params,
		Result: result,
		Chunk:  chunk,
		End:    end,
	}
}

// DescribeNotification registers a notification the daemon pushes. Without it
// the schema would describe only the request/response half of the protocol, and
// every client would have to hand-write `stream.chunk`, `stream.end` and the
// state broadcasts.
func DescribeNotification(method string, params any) {
	notificationRegistry[method] = NotificationDescription{Method: method, Params: params}
}

// Methods returns a copy of the method registry.
func Methods() map[string]Description {
	return copyOf(registry)
}

// Notifications returns a copy of the notification registry.
func Notifications() map[string]NotificationDescription {
	return copyOf(notificationRegistry)
}

// copyOf copies a registry, so a caller iterating it cannot race a registration
// from another package's init.
func copyOf[V any](registry map[string]V) map[string]V {
	copied := make(map[string]V, len(registry))
	for name, entry := range registry {
		copied[name] = entry
	}
	return copied
}

// MethodNames returns every registered method name, sorted.
func MethodNames() []string { return sortedKeys(registry) }

// NotificationNames returns every registered notification name, sorted.
func NotificationNames() []string { return sortedKeys(notificationRegistry) }

// sortedKeys is the one ordering the schema document and its tests rely on.
func sortedKeys[V any](registry map[string]V) []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MethodSchema is one entry of the document's methods map. The two stream
// fields are absent for every method that answers in one response.
type MethodSchema struct {
	Params      json.RawMessage `json:"params"`                 // a $ref into definitions
	Result      json.RawMessage `json:"result"`                 // a $ref into definitions
	StreamChunk json.RawMessage `json:"stream_chunk,omitempty"` // stream.chunk payload
	StreamEnd   json.RawMessage `json:"stream_end,omitempty"`   // stream.end payload
}

// NotificationSchema is one entry of the document's notifications map: the
// params a client decodes when that notification arrives.
type NotificationSchema struct {
	Params json.RawMessage `json:"params"`
}

// Document is the bundle `oberth daemon schema` prints and
// docs/schema/protocol.schema.json holds.
type Document struct {
	Schema          string                        `json:"$schema"`
	ID              string                        `json:"$id"`
	Title           string                        `json:"title"`
	Description     string                        `json:"description"`
	ProtocolVersion string                        `json:"protocol_version"`
	Definitions     map[string]json.RawMessage    `json:"definitions"`
	Methods         map[string]MethodSchema       `json:"methods"`
	Notifications   map[string]NotificationSchema `json:"notifications"`
}

// schemaID is the document's canonical $id. It is a location, not a fetchable
// URL: the file ships inside the release.
const schemaID = "https://github.com/sheathedsharp/option-berth/engine/docs/schema/protocol.schema.json"

// namedTypes are the data-model definitions the contract requires by name. They
// are reflected explicitly so they exist even when no method references them
// yet, which is what lets a client generate types for the whole data model from
// a document whose methods happen to be sparse.
func namedTypes() []any {
	return []any{
		state.Port{}, state.Group{},
		state.Session{}, state.SessionRecord{},
		state.Snapshot{}, state.Delta{}, state.Event{}, state.StateChanged{}, state.StreamSnapshot{},
		state.StreamWorktree{}, state.StreamService{}, state.StreamPort{}, state.EventSource{}, state.Service{},
		state.Stats{}, state.Health{}, state.Docker{}, state.Run{},
		state.PortHolder{},

		StateSnapshotResult{},
		state.KillResult{},
		Error{}, MutationResult{}, KillEnvelope{}, DoctorCheck{},

		// The streaming envelopes. A client needs the shape of {id, data} to
		// read any stream at all.
		StreamChunk{}, StreamEnd{}, StreamStart{},
	}
}

// BuildSchema reflects every registered type into one JSON Schema document.
func BuildSchema() *Document {
	refs := newReflector()

	for _, model := range namedTypes() {
		refs.add(model)
	}
	methods := make(map[string]MethodSchema, len(registry))
	for name, described := range registry {
		methods[name] = MethodSchema{
			Params:      refs.add(described.Params),
			Result:      refs.add(described.Result),
			StreamChunk: refs.add(described.Chunk),
			StreamEnd:   refs.add(described.End),
		}
	}
	notifications := make(map[string]NotificationSchema, len(notificationRegistry))
	for name, described := range notificationRegistry {
		notifications[name] = NotificationSchema{Params: refs.add(described.Params)}
	}

	return &Document{
		Schema:          "https://json-schema.org/draft/2020-12/schema",
		ID:              schemaID,
		Title:           "option-berth daemon protocol",
		Description:     "Generated from the Go types in internal/state and internal/daemon/rpc. Do not edit by hand; run `go generate ./...`.",
		ProtocolVersion: ProtocolVersion,
		Definitions:     refs.definitions,
		Methods:         methods,
		Notifications:   notifications,
	}
}

// Marshal returns the canonical bytes written to
// docs/schema/protocol.schema.json and printed by `oberth daemon schema`.
func Marshal() []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(BuildSchema()); err != nil {
		// The document is built from Go types at hand; an encoding failure means
		// the generator itself is broken, and a half-written schema file is worse
		// than no output.
		panic(fmt.Sprintf("rpc: marshalling the protocol schema: %v", err))
	}
	return buffer.Bytes()
}

// reflector accumulates definitions across many reflections, rewriting
// invopop's "$defs" references to the "definitions" the contract names.
type reflector struct {
	engine      *jsonschema.Reflector
	definitions map[string]json.RawMessage
}

func newReflector() *reflector {
	return &reflector{
		// Additional properties are allowed so that additive protocol changes
		// and the deprecated flat fields `oberth list --json` still carries do
		// not fail validation.
		engine:      &jsonschema.Reflector{AllowAdditionalProperties: true},
		definitions: map[string]json.RawMessage{},
	}
}

// reflection is the subset of invopop's output the reflector needs: what was
// produced, and what definitions it dragged along.
type reflection struct {
	Ref         string                     `json:"$ref"`
	Definitions map[string]json.RawMessage `json:"$defs"`
}

// add reflects v, merges its definitions into the document, and returns a $ref
// pointing at v. A nil v yields nil, so an optional streaming field stays absent
// from the document instead of becoming an empty object.
func (r *reflector) add(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(r.engine.Reflect(v))
	if err != nil {
		panic(fmt.Sprintf("rpc: reflecting %T: %v", v, err))
	}
	raw = rewriteRefs(raw)

	var produced reflection
	if err := json.Unmarshal(raw, &produced); err != nil {
		panic(fmt.Sprintf("rpc: decoding the reflected schema for %T: %v", v, err))
	}
	for name, definition := range produced.Definitions {
		r.definitions[sanitizeDefName(name)] = definition
	}
	if produced.Ref == "" {
		panic(fmt.Sprintf("rpc: %T did not reflect to a referenced definition", v))
	}
	return json.RawMessage(fmt.Sprintf("{%q:%q}", "$ref", produced.Ref))
}

// definitionRef matches a pointer into invopop's "$defs" section.
var definitionRef = regexp.MustCompile(`"#/definitions/([^"]+)"`)

// rewriteRefs turns invopop's output into the document's vocabulary: its
// `#/$defs/` pointers become `#/definitions/`, and the generic instantiation
// names inside them are simplified.
func rewriteRefs(raw []byte) []byte {
	raw = bytes.ReplaceAll(raw, []byte(`#/$defs/`), []byte(`#/definitions/`))
	return definitionRef.ReplaceAllFunc(raw, func(match []byte) []byte {
		name := definitionRef.FindSubmatch(match)[1]
		return []byte(`"#/definitions/` + sanitizeDefName(string(name)) + `"`)
	})
}

// sanitizeDefName turns Go's generic instantiation names into plain identifiers
// a TypeScript or Rust generator can use, recursing through nested arguments:
//
//	Change[github.com/sheathedsharp/option-berth/engine/internal/state.Port]
//	  becomes ChangePort
func sanitizeDefName(name string) string {
	open := strings.IndexByte(name, '[')
	if open < 0 {
		return name
	}
	var simplified strings.Builder
	simplified.WriteString(name[:open])
	arguments := strings.TrimSuffix(name[open+1:], "]")
	for _, argument := range strings.Split(arguments, ",") {
		argument = strings.TrimSpace(argument)
		if dot := strings.LastIndexByte(argument, '.'); dot >= 0 {
			argument = argument[dot+1:]
		}
		simplified.WriteString(sanitizeDefName(argument))
	}
	return simplified.String()
}

// init registers the core namespaces. The packages that own the share, map,
// sessions and claims namespaces register theirs from their own init().
func init() {
	// Daemon lifecycle.
	Describe("daemon.hello", DaemonHelloParams{}, DaemonHelloResult{}, nil, nil)
	Describe("daemon.shutdown", Empty{}, OKResult{}, nil, nil)
	Describe("daemon.status", Empty{}, DaemonStatusResult{}, nil, nil)
	Describe("daemon.schema", Empty{}, DaemonSchemaResult{}, nil, nil)
	Describe("daemon.doctor", DaemonDoctorParams{}, DaemonDoctorResult{}, nil, nil)

	// State: a one-shot snapshot, a subscription, and cancelling either.
	Describe("state.snapshot", StateSnapshotParams{}, StateSnapshotResult{}, nil, nil)
	Describe("state.subscribe", StateSubscribeParams{}, StateSnapshotResult{}, nil, nil)
	Describe("state.unsubscribe", Empty{}, OKResult{}, nil, nil)
	Describe("stream.cancel", StreamCancel{}, OKResult{}, nil, nil)

	// Ports used by the project lifecycle. ports.logs streams the log lines and
	// ends with a status rather than a payload.
	Describe("ports.kill", PortsKillParams{}, KillEnvelope{}, nil, nil)
	Describe("ports.logs", PortsLogsParams{}, PortsLogsResult{}, PortsLogsChunk{}, StreamEnd{})

	// Groups: listing, killing, the two long-running start paths, and the
	// manifest edits that change what a group is.
	Describe("groups.list", Empty{}, GroupsListResult{}, nil, nil)
	Describe("groups.code", GroupsCodeParams{}, GroupsCodeResult{}, nil, nil)
	Describe("groups.kill", GroupsKillParams{}, KillEnvelope{}, nil, nil)
	Describe("groups.start", GroupsStartParams{}, GroupsStartResult{}, GroupsStartChunk{}, GroupsStartEnd{})
	Describe("groups.rename", GroupsRenameParams{}, GroupsRenameResult{}, nil, nil)
	Describe("groups.reload", Empty{}, GroupsReloadResult{}, nil, nil)
	Describe("groups.config.get", GroupsConfigGetParams{}, GroupsConfigGetResult{}, nil, nil)
	Describe("groups.config.set", GroupsConfigSetParams{}, GroupsConfigSetResult{}, nil, nil)
	Describe("groups.init", GroupsInitParams{}, GroupsInitResult{}, nil, nil)

	// Runs: what `oberth start` registers, and what the daemon reports back.
	Describe("runs.register", RunsRegisterParams{}, RunsRegisterResult{}, nil, nil)
	Describe("runs.unregister", RunsUnregisterParams{}, OKResult{}, nil, nil)
	Describe("runs.list", Empty{}, RunsListResult{}, nil, nil)
	Describe("runs.spawn", RunsSpawnParams{}, RunsSpawnResult{}, nil, nil)

	Describe("sessions.kill", SessionsKillParams{}, KillEnvelope{}, nil, nil)

	// Workplaces: the worktree's recorded shell and agent work. The handlers live
	// in internal/workplace.
	Describe("workplace.list", WorkplaceParams{}, WorkplaceListResult{}, nil, nil)
	Describe("workplace.new", WorkplaceNewParams{}, WorkplaceNewResult{}, nil, nil)
	Describe("workplace.use", WorkplaceUseParams{}, WorkplaceUseResult{}, nil, nil)
	Describe("workplace.show", WorkplaceShowParams{}, WorkplaceShowResult{}, nil, nil)
	Describe("workplace.delete", WorkplaceDeleteParams{}, WorkplaceDeleteResult{}, nil, nil)
	Describe("workplace.record", WorkplaceRecordParams{}, WorkplaceRecordResult{}, nil, nil)

	// Config.
	Describe("config.get", Empty{}, ConfigGetResult{}, nil, nil)
	Describe("config.set", ConfigSetParams{}, ConfigSetResult{}, nil, nil)
	Describe("config.path", Empty{}, ConfigPathResult{}, nil, nil)

	// Notifications the daemon pushes. state.subscribe keeps its own broadcast
	// names; every streaming method shares stream.chunk and stream.end, whose
	// `data` is the chunk or end type of the method that opened the stream.
	// state.changed is the redacted scoped stream; the legacy state.delta and
	// state.event remain for unscoped clients.
	DescribeNotification(MethodStateDelta, state.Delta{})
	DescribeNotification(MethodStateEvent, state.Event{})
	DescribeNotification(MethodStateChanged, state.StateChanged{})
	DescribeNotification(MethodStreamChunk, StreamChunk{})
	DescribeNotification(MethodStreamEnd, StreamEnd{})
}
