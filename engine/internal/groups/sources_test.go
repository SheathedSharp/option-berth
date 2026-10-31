package groups

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSourcesParseAndValidate(t *testing.T) {
	cfg, err := loadString(t, "web", `
name: web
services:
  - name: web
    cmd: npm start
    cwd: app
    sources: [src, ../package.json]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Services[0].Sources; !reflect.DeepEqual(got, []string{"src", "../package.json"}) {
		t.Fatalf("sources = %v", got)
	}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"absolute", "sources: [/etc/passwd]", "must be relative"},
		{"escape", "sources: [../../secret]", "escapes the directory"},
		{"empty path", "sources: ['']", "empty path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadString(t, "web", "\nname: web\nservices:\n  - name: web\n    cmd: npm start\n    "+tc.body+"\n")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// The distinction is the contract: an omitted key means "the service
// directory", and an explicit empty list opts a hot-reloading service out.
func TestSourcesOmittedAndEmptyAreDifferent(t *testing.T) {
	omitted, err := loadString(t, "web", `
name: web
services:
  - name: web
    cmd: npm start
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if omitted.Services[0].Sources != nil {
		t.Fatalf("omitted sources = %#v, want nil", omitted.Services[0].Sources)
	}

	disabled, err := loadString(t, "web", `
name: web
services:
  - name: web
    cmd: npm start
    sources: []
`)
	if err != nil {
		t.Fatalf("Load with sources: []: %v", err)
	}
	if got := disabled.Services[0].Sources; got == nil || len(got) != 0 {
		t.Fatalf("explicit sources: [] = %#v, want a non-nil empty slice", got)
	}
}

func TestServiceRowPreservesSourcesDefaultVersusDisabled(t *testing.T) {
	def := ServiceRow(Service{Name: "web", Cmd: "serve"})
	if def.Sources != nil {
		t.Fatalf("default row sources = %#v, want nil (JSON null)", def.Sources)
	}
	off := ServiceRow(Service{Name: "web", Cmd: "serve", Sources: []string{}})
	if off.Sources == nil || len(off.Sources) != 0 {
		t.Fatalf("disabled row sources = %#v, want an empty array", off.Sources)
	}
	on := ServiceRow(Service{Name: "web", Cmd: "serve", Sources: []string{"src"}})
	if !reflect.DeepEqual(on.Sources, []string{"src"}) {
		t.Fatalf("row sources = %v", on.Sources)
	}
}

func TestServiceNodeWritesSourcesIncludingTheEmptyOptOut(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []string
		want    string
		absent  bool
	}{
		{"omitted", nil, "", true},
		{"list", []string{"src", "go.mod"}, "sources: [src, go.mod]\n", false},
		{"disabled", []string{}, "sources: []\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := yaml.Marshal(serviceNode(ServiceAdd{Name: "web", Cmd: "serve", Sources: tc.sources}))
			if err != nil {
				t.Fatal(err)
			}
			text := string(out)
			if tc.absent && strings.Contains(text, "sources:") {
				t.Fatalf("omitted sources were written:\n%s", text)
			}
			if !tc.absent && !strings.Contains(text, tc.want) {
				t.Fatalf("output does not contain %q:\n%s", tc.want, text)
			}
		})
	}
}

// A rename or draft merge re-encodes the service. `sources: []` must survive
// that round-trip; dropping it would silently re-enable a check the author
// opted out of.
func TestMarshalYAMLKeepsSourcesDefaultAndOptOut(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []string
		want    string
		absent  bool
	}{
		{"omitted", nil, "", true},
		{"list", []string{"src", "go.mod"}, "sources: [src, go.mod]\n", false},
		{"disabled", []string{}, "sources: []\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := yaml.Marshal(Service{Name: "web", Cmd: "serve", Sources: tc.sources})
			if err != nil {
				t.Fatal(err)
			}
			text := string(out)
			if tc.absent && strings.Contains(text, "sources:") {
				t.Fatalf("omitted sources were written:\n%s", text)
			}
			if !tc.absent && !strings.Contains(text, tc.want) {
				t.Fatalf("output does not contain %q:\n%s", tc.want, text)
			}
			var back Service
			if err := yaml.Unmarshal(out, &back); err != nil {
				t.Fatal(err)
			}
			if (back.Sources == nil) != (tc.sources == nil) || len(back.Sources) != len(tc.sources) {
				t.Fatalf("round-trip sources = %#v, want %#v", back.Sources, tc.sources)
			}
		})
	}
}

// Where to look for code is not part of the executable process spec: changing
// `sources:` must not make a running service look like it was started from an
// old manifest and demand a restart.
func TestSourcesDoNotChangeTheRuntimeSpecHash(t *testing.T) {
	a := Service{Name: "web", Cmd: "serve", Sources: []string{"src"}}
	b := Service{Name: "web", Cmd: "serve", Sources: []string{"lib"}}
	c := Service{Name: "web", Cmd: "serve"}
	if ServiceSpecHash(a) != ServiceSpecHash(b) || ServiceSpecHash(a) != ServiceSpecHash(c) {
		t.Fatal("sources changed the runtime spec hash")
	}
}
