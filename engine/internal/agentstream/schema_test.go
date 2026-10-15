package agentstream_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestFixtureLinesMatchSchema(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "docs", "schema", "agent-stream.schema.json")
	fixtureDir := filepath.Join("testdata")
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := compileSchema(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ndjson") {
			continue
		}
		path := filepath.Join(fixtureDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var value any
			if err := json.Unmarshal([]byte(line), &value); err != nil {
				t.Fatalf("%s:%d invalid json: %v", entry.Name(), lineNumber+1, err)
			}
			if err := schema.Validate(value); err != nil {
				t.Fatalf("%s:%d schema: %v", entry.Name(), lineNumber+1, err)
			}
		}
	}
}

func compileSchema(path string) (*jsonschema.Schema, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("agent-stream.schema.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("agent-stream.schema.json")
}
