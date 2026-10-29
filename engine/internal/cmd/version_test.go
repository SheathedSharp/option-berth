package cmd

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/versioncheck"
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestVersionJSONCarriesBuildIdentity(t *testing.T) {
	previous := versionJSONFlag
	versionJSONFlag = true
	t.Cleanup(func() { versionJSONFlag = previous })

	out := captureStdout(t, func() {
		if err := versionRun(testCommand(), nil); err != nil {
			t.Fatalf("versionRun: %v", err)
		}
	})

	var got versionDocument
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("version JSON: %v\n%s", err, out)
	}
	if got.Version == "" || got.Platform == "" {
		t.Fatalf("version document = %+v, want version and platform", got)
	}
}

func TestRootShortVersionFlag(t *testing.T) {
	previous := versionFlag
	versionFlag = true
	t.Cleanup(func() { versionFlag = previous })

	out := captureStdout(t, func() {
		if err := rootCmd.RunE(rootCmd, nil); err != nil {
			t.Fatalf("root version: %v", err)
		}
	})
	got := strings.TrimSpace(out)
	if got == "" || strings.Contains(got, "commit") || strings.Contains(got, "built") {
		t.Fatalf("short version output = %q, want only the CLI version", got)
	}
}

// The first screen is where the brand shows up, so the mark has to be the real
// one. markRail and markToken are generated (mark_gen.go) from the client's
// geometry, and a regeneration that came out empty or misshapen would otherwise
// ship silently.
func TestWelcomeShowsTheBrandMarkBesideTheWordmark(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	// paintMarkRow reads a cell's shape from the same column in both layers, so
	// the generator has to keep them the same shape. If it ever stops, the
	// colour would land on the wrong cells rather than fail loudly.
	rail := strings.Split(strings.TrimSuffix(markRail, "\n"), "\n")
	token := strings.Split(strings.TrimSuffix(markToken, "\n"), "\n")
	if len(rail) != len(token) {
		t.Fatalf("mark layers have %d and %d rows, want the same", len(rail), len(token))
	}
	mark := make([]string, len(rail))
	for i := range rail {
		if r, tk := []rune(rail[i]), []rune(token[i]); len(r) != len(tk) {
			t.Fatalf("mark row %d is %d and %d cells wide, want the same", i, len(r), len(tk))
		}
		mark[i] = strings.TrimRight(paintMarkRow(rail[i], token[i]), " ")
	}
	if len(mark) < 2 || strings.TrimSpace(strings.Join(mark, "")) == "" {
		t.Fatalf("mark = %q, want a multi-row mark", mark)
	}

	var buf bytes.Buffer
	printWelcome(&buf, versioncheck.State{Current: "v1.1.2"})
	lines := strings.Split(buf.String(), "\n")
	if len(lines) < len(mark) {
		t.Fatalf("welcome has %d rows, want at least the %d mark rows", len(lines), len(mark))
	}
	width := 0
	for _, row := range mark {
		if w := display.DisplayWidth(row); w > width {
			width = w
		}
	}
	for i, row := range mark {
		if !strings.HasPrefix(lines[i], "  "+row) {
			t.Errorf("welcome row %d = %q, want mark row %q on the two-space margin",
				i, lines[i], row)
		}
	}
	// The wordmark and the tagline sit beside the mark, not under it: they
	// start in the column just past the mark's widest row.
	for i, want := range append([]string{wordmark}, taglineLines...) {
		prefix := "  " + display.PadDisplay(mark[i], width) + "   "
		if !strings.HasPrefix(lines[i], prefix) || !strings.HasSuffix(lines[i], want) {
			t.Errorf("welcome row %d = %q, want %q in the column after the mark",
				i, lines[i], want)
		}
	}
}

// The mark is drawn in the brand's two colours — rails in ink/paper, capsule in
// terracotta. Painting it in one colour is what made the capsule read as a
// third blob beside the two rail caps, so which layer owns a cell is the point.
// Whichever colours the terminal ends up with, the glyphs must not move.
func TestMarkLayersPaintEveryCellExactlyOnce(t *testing.T) {
	rail := strings.Split(strings.TrimSuffix(markRail, "\n"), "\n")
	token := strings.Split(strings.TrimSuffix(markToken, "\n"), "\n")
	if len(rail) != len(token) {
		t.Fatalf("mark layers have %d and %d rows, want the same", len(rail), len(token))
	}
	var sawRail, sawToken bool
	for i := range rail {
		r, tk := []rune(rail[i]), []rune(token[i])
		if len(r) != len(tk) {
			t.Fatalf("mark row %d is %d and %d cells wide, want the same", i, len(r), len(tk))
		}
		merged := make([]rune, len(r))
		for j := range r {
			switch {
			case r[j] != ' ' && tk[j] != ' ':
				t.Fatalf("mark row %d cell %d is claimed by both layers", i, j)
			case r[j] != ' ':
				merged[j], sawRail = r[j], true
			case tk[j] != ' ':
				merged[j], sawToken = tk[j], true
			default:
				merged[j] = ' '
			}
		}
		if got := ansiEscape.ReplaceAllString(paintMarkRow(rail[i], token[i]), ""); got != string(merged) {
			t.Errorf("painted row %d = %q, want the mark %q", i, got, string(merged))
		}
	}
	if !sawRail || !sawToken {
		t.Fatalf("mark has rail=%v token=%v, want both shapes present", sawRail, sawToken)
	}
}

// The bare `oberth` screen is a signpost, not the manual: the logo, one line
// of what this is, the commands a session starts with, and the way out to the
// full grouped help.
func TestPrintWelcomeShowsQuickStartNotTheManual(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	var buf bytes.Buffer
	printWelcome(&buf, versioncheck.State{Current: "v1.1.2"})
	out := buf.String()

	for _, want := range []string{
		"oberth status", "oberth up", "oberth down",
		"Start here:", "Learn more:", "oberth --help",
		// the one-line identity
		"manifest and runtime",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("welcome is missing %q:\n%s", want, out)
		}
	}
	// The command column lines up: every description starts at the same cell.
	lines := strings.Split(out, "\n")
	desc := -1
	for _, l := range lines {
		if !strings.HasPrefix(l, "    oberth ") {
			continue
		}
		// The description starts after the padded command and its spaces.
		cmdLen := 4
		for cmdLen < len(l) && l[cmdLen] != ' ' {
			cmdLen++
		}
		for cmdLen < len(l) && l[cmdLen] == ' ' {
			cmdLen++
		}
		if desc == -1 {
			desc = cmdLen
		} else if desc != cmdLen {
			t.Fatalf("description column drifted between rows:\n%s", out)
		}
	}
	// The manual is elsewhere: no grouped headers, no full flag block. The
	// headers named here are the layers docs/cli.md documents, so this test
	// fails if the welcome screen starts carrying the manual's grouping.
	for _, gone := range []string{"Facts:", "Operations:", "Code and declaration:", "Handoff:", "Usage:", "Flags:"} {
		if strings.Contains(out, gone) {
			t.Errorf("welcome should not carry the manual's %q:\n%s", gone, out)
		}
	}
}

// The signpost says which build is running, and — only when a check actually
// succeeded — whether that build is behind the published release. An unknown
// answer must not read as "latest", and an older release must put the upgrade
// path at the bottom where the reader looks after the commands.
func TestWelcomeVersionLineAndUpgradeHint(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	for _, test := range []struct {
		name   string
		state  versioncheck.State
		want   []string
		absent []string
	}{
		{
			name:   "same build is the latest",
			state:  versioncheck.State{Current: "v1.1.2", Latest: "v1.1.2"},
			want:   []string{"v1.1.2 (latest)"},
			absent: []string{"latest version", "Upgrade to"},
		},
		{
			name:  "newer release carries the upgrade path",
			state: versioncheck.State{Current: "v1.1.2", Latest: "v1.2.0"},
			want: []string{
				"v1.1.2 (latest version v1.2.0)",
				"Upgrade to v1.2.0:",
				"install.sh",
				"oberth daemon stop",
			},
			absent: []string{"(latest)"},
		},
		{
			name:   "a failed check shows only the version",
			state:  versioncheck.State{Current: "v1.1.2", Error: "network down"},
			want:   []string{"v1.1.2"},
			absent: []string{"(latest", "Upgrade to"},
		},
		{
			name:   "a build ahead of the feed is not told to downgrade",
			state:  versioncheck.State{Current: "v1.2.0", Latest: "v1.1.2"},
			want:   []string{"v1.2.0"},
			absent: []string{"(latest", "Upgrade to"},
		},
		{
			name:   "development build is not compared",
			state:  versioncheck.State{Current: "dev"},
			want:   []string{"dev"},
			absent: []string{"(latest", "Upgrade to"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			printWelcome(&buf, test.state)
			out := buf.String()
			for _, want := range test.want {
				if !strings.Contains(out, want) {
					t.Errorf("welcome is missing %q:\n%s", want, out)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(out, absent) {
					t.Errorf("welcome should not carry %q:\n%s", absent, out)
				}
			}
		})
	}
}

func TestVersionDocumentCarriesLatestOnlyWhenChecked(t *testing.T) {
	if plain := versionDocumentFor(nil); plain.Latest != nil {
		t.Fatalf("plain version document has latest = %+v, want nil", plain.Latest)
	}

	checked := versionDocumentFor(&versioncheck.State{
		Current:   "v1.1.2",
		Latest:    "v1.2.0",
		CheckedAt: time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC),
	})
	data, err := json.Marshal(checked)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Latest struct {
			Version         string `json:"version"`
			CheckedAt       string `json:"checked_at"`
			UpdateAvailable bool   `json:"update_available"`
		} `json:"latest"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("version JSON: %v\n%s", err, data)
	}
	if got.Latest.Version != "v1.2.0" || got.Latest.CheckedAt != "2026-10-29T08:00:00Z" {
		t.Errorf("latest = %+v, want the checked release and its UTC time", got.Latest)
	}
	if !got.Latest.UpdateAvailable {
		t.Errorf("update_available = false, want true for v1.1.2 against v1.2.0")
	}
}

func TestLatestLineDistinguishesStates(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	for _, test := range []struct {
		name  string
		state versioncheck.State
		want  string
	}{
		{"same", versioncheck.State{Current: "v1.1.2", Latest: "v1.1.2"}, "latest v1.1.2 (up to date)"},
		{"behind", versioncheck.State{Current: "v1.1.2", Latest: "v1.2.0"}, "latest v1.2.0 (update available)"},
		{"ahead", versioncheck.State{Current: "v1.2.0", Latest: "v1.1.2"}, "latest v1.1.2 (this build is newer)"},
		{"unknown", versioncheck.State{Current: "dev", Latest: "v1.2.0"}, "latest v1.2.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := latestLine(test.state); got != test.want {
				t.Fatalf("latestLine = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUpgradeCommandMatchesPlatform(t *testing.T) {
	for _, test := range []struct {
		goos string
		want string
	}{
		{"darwin", "install.sh"},
		{"linux", "install.sh"},
		{"windows", "install.ps1"},
	} {
		if got := upgradeCommand(test.goos); !strings.Contains(got, test.want) {
			t.Errorf("upgradeCommand(%q) = %q, want %q", test.goos, got, test.want)
		}
	}
}
