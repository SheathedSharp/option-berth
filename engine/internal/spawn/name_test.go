package spawn

import (
	"slices"
	"testing"
)

func TestInferName(t *testing.T) {
	cases := []struct {
		label string
		argv  []string
		want  string
	}{
		{"npm run", []string{"npm", "run", "serve"}, "serve"},
		{"pnpm run", []string{"pnpm", "run", "web"}, "web"},
		{"yarn shorthand", []string{"yarn", "dev"}, "dev"},
		{"bun run", []string{"bun", "run", "start"}, "start"},
		{"npx bin", []string{"npx", "vite"}, "vite"},
		{"uv run", []string{"uv", "run", "api"}, "api"},
		{"uv nested", []string{"uv", "run", "uvicorn", "app:app"}, "uvicorn"},
		{"python module", []string{"python", "-m", "uvicorn"}, "uvicorn"},
		{"python dotted module", []string{"python3", "-m", "http.server", "8000"}, "http.server"},
		{"relative script", []string{"./run.sh"}, "run.sh"},
		{"absolute script", []string{"/usr/local/bin/run.sh"}, "run.sh"},
		{"compose service", []string{"docker", "compose", "up", "cache"}, "cache"},
		{"compose v1", []string{"docker-compose", "up", "worker"}, "worker"},
		{"go run path", []string{"go", "run", "./cmd/api"}, "api"},
		{"cargo bin", []string{"cargo", "run", "--bin", "gateway"}, "gateway"},
		{"value flag", []string{"npm", "--prefix", "./web", "run", "serve"}, "serve"},
		{"assignment first", []string{"PORT=3000", "npm", "run", "serve"}, "serve"},
		{"poetry run", []string{"poetry", "run", "flask"}, "flask"},
		{"node script", []string{"node", "server.js"}, "server.js"},
		{"plain binary", []string{"redis-server"}, "redis-server"},
		{"manager alone", []string{"npm"}, "npm"},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := InferName(tc.argv); got != tc.want {
				t.Errorf("InferName(%q) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}

func TestSplitCmd(t *testing.T) {
	cases := []struct {
		label string
		in    string
		want  []string
	}{
		{"simple", "npm run dev", []string{"npm", "run", "dev"}},
		{"padded", "  pnpm   run   web  ", []string{"pnpm", "run", "web"}},
		{"double quotes", `uv run uvicorn "app:app" --reload`, []string{"uv", "run", "uvicorn", "app:app", "--reload"}},
		{"single quotes", `sh -c 'echo hello there'`, []string{"sh", "-c", "echo hello there"}},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			assertSplit(t, SplitCmd(tc.in), tc.want, tc.in)
		})
	}
}

// Outside single quotes a backslash escapes whatever follows it.
func TestSplitCmdUnixEscapes(t *testing.T) {
	cases := []struct {
		label string
		in    string
		want  []string
	}{
		{"escaped space", `/srv/my\ app/serve --port 3100`, []string{"/srv/my app/serve", "--port", "3100"}},
		{"escaped quote", `echo "say \"hi\""`, []string{"echo", `say "hi"`}},
		{"single quotes keep backslash", `echo 'a\b'`, []string{"echo", `a\b`}},
		{"escaped backslash", `echo \\`, []string{"echo", `\`}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			assertSplit(t, splitCmdFor(tc.in, false), tc.want, tc.in)
		})
	}
}

// `\` is a path separator on Windows: treating it as an escape turned
// `C:\Users\me\app.exe` into `C:Usersmeapp.exe`. Only a quote inside double
// quotes is escapable, as CommandLineToArgvW reads it.
func TestSplitCmdWindowsKeepsBackslashes(t *testing.T) {
	cases := []struct {
		label string
		in    string
		want  []string
	}{
		{"unquoted path", `C:\Users\me\app.exe 3100`, []string{`C:\Users\me\app.exe`, "3100"}},
		{"quoted path", `"C:\Program Files\app\app.exe" --port 3100`, []string{`C:\Program Files\app\app.exe`, "--port", "3100"}},
		{"single-quoted path", `'C:\Users\me\app.exe' serve`, []string{`C:\Users\me\app.exe`, "serve"}},
		{"trailing backslash", `app.exe --dir "C:\logs\\" --port 80`, []string{"app.exe", "--dir", `C:\logs\`, "--port", "80"}},
		{"escaped quote", `echo "say \"hi\""`, []string{"echo", `say "hi"`}},
		{"plain", `pnpm run web`, []string{"pnpm", "run", "web"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			assertSplit(t, splitCmdFor(tc.in, true), tc.want, tc.in)
		})
	}
}

func assertSplit(t *testing.T, got, want []string, input string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("split(%q) = %q, want %q", input, got, want)
	}
}
