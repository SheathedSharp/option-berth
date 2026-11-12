package rpc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// contractMethods is every method the daemon describes: the core table,
// every namespace another package registers, and the names
// relay session reserve. It is the whole protocol, not a sample.
//
// The direction is checked both ways (below): a method in this list that the
// daemon does not describe fails, and so does a method the daemon describes
// that this list has never heard of. That second half is what was missing —
// `decide.status` and `decide.configure` shipped without ever being listed, and
// nothing said so (backlog H8).
var contractMethods = []string{
	"daemon.hello", "daemon.shutdown", "daemon.status", "daemon.schema",
	"daemon.doctor",
	"state.snapshot", "state.subscribe", "state.unsubscribe", "stream.cancel",
	"ports.kill", "ports.logs",
	"groups.list", "groups.code", "groups.kill", "groups.start",
	"groups.reload", "groups.init", "groups.rename",
	"groups.config.get", "groups.config.set",
	"runs.register", "runs.unregister", "runs.list", "runs.spawn",
	"sessions.kill",
	"workplace.list", "workplace.new", "workplace.use",
	"workplace.show", "workplace.delete", "workplace.record",
	"config.get", "config.set", "config.path",
}

// schemaDocument decodes the generated document into the generic shape a client
// generator reads it in, so the checks below are about the wire document rather
// than about the Go types it was reflected from.
func schemaDocument(t *testing.T) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(Marshal(), &document); err != nil {
		t.Fatalf("decoding the generated schema: %v", err)
	}
	return document
}

// schemaObject returns one object-valued member of the document, failing when
// the member is absent or is not an object.
func schemaObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	object, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("schema has no %s object", key)
	}
	return object
}

// definitionProperties returns the properties of one named definition, failing
// when the definition or its properties are missing.
func definitionProperties(t *testing.T, definitions map[string]any, name string) map[string]any {
	t.Helper()
	definition, ok := definitions[name].(map[string]any)
	if !ok {
		t.Fatalf("definition %q is missing from the schema", name)
	}
	properties, ok := definition["properties"].(map[string]any)
	if !ok {
		t.Fatalf("definition %q has no properties", name)
	}
	return properties
}

// requireEntries fails for every named entry the collection does not carry.
func requireEntries(t *testing.T, collection string, entries map[string]any, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, ok := entries[name]; !ok {
			t.Errorf("%s[%q] is missing from the schema", collection, name)
		}
	}
}

// requireProperties fails for every property the object does not carry.
func requireProperties(t *testing.T, subject string, object map[string]any, properties ...string) {
	t.Helper()
	for _, property := range properties {
		if _, ok := object[property]; !ok {
			t.Errorf("%s has no %q property", subject, property)
		}
	}
}

func TestEveryContractMethodIsDescribed(t *testing.T) {
	described := Methods()
	for _, method := range contractMethods {
		if _, ok := described[method]; !ok {
			t.Errorf("method %q is in the contract but not described", method)
		}
	}
}

// TestEveryDescribedMethodIsInTheContract is the other direction, and it is the
// one that keeps the list honest: without it a new method joins the wire
// silently and this file stops being a description of the protocol.
func TestEveryDescribedMethodIsInTheContract(t *testing.T) {
	contract := make(map[string]bool, len(contractMethods))
	for _, m := range contractMethods {
		if contract[m] {
			t.Errorf("method %q is listed twice", m)
		}
		contract[m] = true
	}
	for _, m := range MethodNames() {
		if !contract[m] {
			t.Errorf("method %q is described but not in contractMethods", m)
		}
	}
	if len(MethodNames()) != len(contractMethods) {
		t.Errorf("the daemon describes %d methods, contractMethods lists %d",
			len(MethodNames()), len(contractMethods))
	}
}

// expose.* was renamed to share.* and its provider methods deleted before any
// handler served them (docs/SHARE.md in option-berth-relay, "The rename").
func TestNoExposeMethods(t *testing.T) {
	for _, method := range MethodNames() {
		if strings.HasPrefix(method, "expose.") {
			t.Errorf("method %q is described; expose.* was renamed to share.*", method)
		}
	}
	document := string(Marshal())
	for _, retired := range []string{`"expose.`, "ProviderInfo", "ExposeInstallProvider", `"Tunnel"`, "ChangeTunnel"} {
		if strings.Contains(document, retired) {
			t.Errorf("the generated schema still mentions %s", retired)
		}
	}
}

// A streaming method answers with a subscription id and then pushes payloads, so
// a client cannot type its stream unless the description names both payloads.
func TestStreamingMethodsDeclareChunkAndEnd(t *testing.T) {
	const streaming = "groups.start"
	described, ok := Methods()[streaming]
	if !ok {
		t.Fatalf("method %q not described", streaming)
	}
	if described.Chunk == nil || described.End == nil {
		t.Errorf("streaming method %q must declare chunk and end types", streaming)
	}
}

func TestBuildSchemaHasContractDefinitions(t *testing.T) {
	document := schemaDocument(t)

	definitions := schemaObject(t, document, "definitions")
	requireEntries(t, "definitions", definitions,
		"Port", "Group", "Session", "Snapshot", "Delta", "Event", "Error")

	port := definitionProperties(t, definitions, "Port")
	requireProperties(t, "Port", port,
		"display_name", "ppid", "project_root", "group", "group_source",
		"run", "stats", "started_at", "session")
	// The dead share/tunnel/proxy fields were removed (R3.1): they must be gone.
	for _, dead := range []string{"shares", "exposed_urls", "proxy_id", "proxy_target_port"} {
		if _, ok := port[dead]; ok {
			t.Errorf("Port still has dead %q property", dead)
		}
	}

	methods := schemaObject(t, document, "methods")
	for _, method := range contractMethods {
		entry, ok := methods[method].(map[string]any)
		if !ok {
			t.Errorf("methods[%q] missing from schema", method)
			continue
		}
		for _, half := range []string{"params", "result"} {
			if _, ok := entry[half]; !ok {
				t.Errorf("methods[%q] has no %s schema", method, half)
			}
		}
	}

	if document["protocol_version"] != ProtocolVersion {
		t.Errorf("protocol_version = %v, want %s", document["protocol_version"], ProtocolVersion)
	}
}

func TestStateFilterUsesTheAgreedWireField(t *testing.T) {
	filter := state.Scope{Worktrees: []string{"/code/example-worker"}}
	raw, err := json.Marshal(StateSubscribeParams{Events: true, Scope: &filter})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"filter"`) || strings.Contains(text, `"scope"`) {
		t.Fatalf("state.subscribe params = %s, want filter and no scope alias", text)
	}
}

// The document a client reads and the document the daemon generates must be the
// same bytes, so a type change cannot ship beside a stale schema file.
func TestCheckedInSchemaIsUpToDate(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "schema", "protocol.schema.json")
	checkedIn, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go generate ./...`)", err)
	}
	if generated := Marshal(); !bytes.Equal(foldLineEndings(checkedIn), foldLineEndings(generated)) {
		t.Fatalf("%s is stale; run `go generate ./...`", path)
	}
}

// foldLineEndings folds CRLF to LF. .gitattributes pins the checked-in schema to
// LF so the file a Windows CI checkout compares is the file `go generate` wrote,
// but a developer's checkout made before that (or with core.autocrlf forced on)
// still has CRLF on disk, and the schema's content is what this test is about,
// not its line endings.
func foldLineEndings(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// TestSchemaEnumsMatchGoEnums guards against the Go enum constants and the
// struct-tag enums in the generated schema drifting apart.
func TestSchemaEnumsMatchGoEnums(t *testing.T) {
	var document struct {
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(Marshal(), &document); err != nil {
		t.Fatalf("decoding the generated schema: %v", err)
	}

	portTypes := make([]string, 0, len(state.AllPortTypes))
	for _, value := range state.AllPortTypes {
		portTypes = append(portTypes, string(value))
	}
	groupSources := make([]string, 0, len(state.AllGroupSources))
	for _, value := range state.AllGroupSources {
		groupSources = append(groupSources, string(value))
	}

	for _, want := range []struct {
		definition string
		property   string
		enum       []string
	}{
		{"Port", "type", portTypes},
		{"Group", "source", groupSources},
	} {
		assertEnum(t, document.Definitions[want.definition].Properties[want.property], want.enum)
	}
}

// assertEnum checks one property's enum against the Go values it is generated
// from: same members, same order.
func assertEnum(t *testing.T, raw json.RawMessage, want []string) {
	t.Helper()
	var property struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(raw, &property); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	if !slices.Equal(property.Enum, want) {
		t.Fatalf("enum = %v, want %v", property.Enum, want)
	}
}

// contractNotifications is every message the daemon pushes without being asked
// . They are half the protocol, and until they were described a
// client had to hand-write them from prose.
var contractNotifications = []string{
	"state.delta", "state.event", "state.changed", "stream.chunk", "stream.end",
}

func TestEveryNotificationIsDescribed(t *testing.T) {
	described := Notifications()
	for _, name := range contractNotifications {
		if _, ok := described[name]; !ok {
			t.Errorf("notification %q is in the contract but not described", name)
		}
	}
	if names := NotificationNames(); len(names) != len(described) {
		t.Errorf("NotificationNames returned %d of %d notifications", len(names), len(described))
	}
}

// TestSchemaCarriesNotificationsAndTheStreamEnvelope: a generator reading the
// document alone must be able to type every inbound message, envelope included.
func TestSchemaCarriesNotificationsAndTheStreamEnvelope(t *testing.T) {
	document := schemaDocument(t)

	notifications := schemaObject(t, document, "notifications")
	for _, notification := range contractNotifications {
		entry, ok := notifications[notification].(map[string]any)
		if !ok {
			t.Errorf("notifications[%q] missing from schema", notification)
			continue
		}
		if _, ok := entry["params"]; !ok {
			t.Errorf("notifications[%q] has no params schema", notification)
		}
	}

	definitions := schemaObject(t, document, "definitions")
	requireEntries(t, "definitions", definitions, "StreamChunk", "StreamEnd", "StreamStart")

	chunk := definitionProperties(t, definitions, "StreamChunk")
	requireProperties(t, "StreamChunk", chunk, "id", "data")
}
