package spawn

import (
	"encoding/json"
	"strings"
	"testing"
)

// inferCases go through SplitCmd and InferName together, because that is how
// the product reaches them: a `cmd:` line is split, and the name comes from the
// argv it became. Testing the pair here means a quoting change that shifts an
// argument boundary shows up as a wrong name rather than as a test that still
// passes because it built its own argv.
var inferCases = []struct {
	line string // an `oberth.yaml` cmd: line
	want string
}{
	{"npm run dev", "dev"},
	{"pnpm run web", "web"},
	{"yarn dev", "dev"},
	{"bun run start", "start"},
	{"npx vite", "vite"},
	{"uv run api", "api"},
	{"uv run uvicorn app:app", "uvicorn"},
	{"python -m uvicorn", "uvicorn"},
	{"python3 -m http.server 8000", "http.server"},
	{"./dev.sh", "dev.sh"},
	{"/usr/local/bin/dev.sh", "dev.sh"},
	{"docker compose up db", "db"},
	{"docker-compose up worker", "worker"},
	{"go run ./cmd/api", "api"},
	{"cargo run --bin server", "server"},
	{"npm --prefix ./web run dev", "dev"},
	{"PORT=3000 npm run dev", "dev"},
	{"poetry run flask", "flask"},
	{"node server.js", "server.js"},
	{"redis-server", "redis-server"},
	{"npm", "npm"},
	{"DEBUG=1 node worker.js", "worker.js"},
	{"NPM.EXE run dev", "dev"},
	// An unknown command names itself: `sh` is not a launcher, so the script
	// it is handed says nothing about the run.
	{`sh -c 'npm run dev'`, "sh"},
	{"", ""},
}

func TestInferName(t *testing.T) {
	for _, tc := range inferCases {
		if got := InferName(SplitCmd(tc.line)); got != tc.want {
			t.Errorf("InferName(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// splitCases state the expected argv as a JSON array: the quoting in a test
// table is then readable without counting nested []string{...} literals, and a
// mismatch prints both sides in the same notation.
var splitCases = []struct {
	line    string
	want    string // JSON array
	windows bool
}{
	{line: "npm run dev", want: `["npm","run","dev"]`},
	{line: "  npm   run   dev  ", want: `["npm","run","dev"]`},
	{line: `uv run uvicorn "app:app" --reload`, want: `["uv","run","uvicorn","app:app","--reload"]`},
	{line: `sh -c 'echo hi there'`, want: `["sh","-c","echo hi there"]`},
	{line: "", want: `[]`},

	// POSIX: outside single quotes a backslash escapes whatever follows it.
	{line: `/tmp/a\ b/serve --port 3000`, want: `["/tmp/a b/serve","--port","3000"]`},
	{line: `echo "say \"hi\""`, want: `["echo","say \"hi\""]`},
	{line: `echo 'a\b'`, want: `["echo","a\\b"]`},
	{line: `echo \\`, want: `["echo","\\"]`},

	// Windows: `\` is a path separator. Reading it as an escape turned
	// `C:\Users\me\listener.exe` into `C:Usersmelistener.exe`, which no
	// `oberth up` could start.
	{line: `C:\Users\me\listener.exe 3000`, want: `["C:\\Users\\me\\listener.exe","3000"]`, windows: true},
	{line: `"C:\Program Files\app\app.exe" --port 3000`, want: `["C:\\Program Files\\app\\app.exe","--port","3000"]`, windows: true},
	{line: `'C:\Users\me\app.exe' serve`, want: `["C:\\Users\\me\\app.exe","serve"]`, windows: true},
	{line: `app.exe --dir "C:\logs\\" --port 80`, want: `["app.exe","--dir","C:\\logs\\","--port","80"]`, windows: true},
	{line: `echo "say \"hi\""`, want: `["echo","say \"hi\""]`, windows: true},
	{line: "npm run dev", want: `["npm","run","dev"]`, windows: true},
}

func TestSplitCmd(t *testing.T) {
	for _, tc := range splitCases {
		platform := "unix"
		if tc.windows {
			platform = "windows"
		}
		t.Run(platform+"/"+tc.line, func(t *testing.T) {
			compareArgs(t, splitCmdFor(tc.line, tc.windows), tc.want, tc.line)
		})
	}
}

// TestSplitCmdReachesTheSameAnswerEverywhere guards the cases whose answer
// cannot depend on the platform: ordinary quoting has to behave identically, or
// a manifest written on one machine starts differently on the other.
func TestSplitCmdReachesTheSameAnswerEverywhere(t *testing.T) {
	for _, line := range []string{`a "b c" 'd e'`, "a b  c", `""`, `a ""`} {
		unix := SplitCmd(line)
		windows := splitCmdFor(line, true)
		if strings.Join(unix, "\x00") != strings.Join(windows, "\x00") {
			t.Errorf("split(%q) = %q on unix, %q on windows", line, unix, windows)
		}
	}
}

// TestLauncherTableParses guards the hand-written table: every launcher line has
// to become a lookup key, and no key may look like a path or a quoted word.
func TestLauncherTableParses(t *testing.T) {
	if len(launcherVerbs) < 20 {
		t.Fatalf("launcher table parsed only %d launchers", len(launcherVerbs))
	}
	for line := range strings.SplitSeq(launcherTables, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		launcher := strings.Fields(line)[0]
		if _, ok := launcherVerbs[launcher]; !ok {
			t.Errorf("launcher %q is in the table but did not parse", launcher)
		}
		if strings.ContainsAny(launcher, "/\\\"'=") {
			t.Errorf("launcher %q cannot be a binary name", launcher)
		}
	}
}

// compareArgs reports a split mismatch in the same JSON notation the table is
// written in, so the failure is readable next to the case that produced it.
func compareArgs(t *testing.T, got []string, wantJSON, line string) {
	t.Helper()
	var want []string
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatalf("bad expectation %s: %v", wantJSON, err)
	}
	if len(got) != len(want) {
		t.Fatalf("split(%q) = %s, want %s", line, renderArgs(got), wantJSON)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("split(%q) = %s, want %s", line, renderArgs(got), wantJSON)
		}
	}
}

func renderArgs(args []string) string {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "<?>"
	}
	return string(encoded)
}
