package groups

import (
	"encoding/json"
	"testing"
)

// roundTrip marshals a value and decodes it back into a generic tree: the shape
// the JSON Schema validator is handed.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	encoded, marshalErr := json.Marshal(v)
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}
	var decoded any
	if decodeErr := json.Unmarshal(encoded, &decoded); decodeErr != nil {
		t.Fatalf("unmarshal: %v", decodeErr)
	}
	return decoded
}
