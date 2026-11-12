package ports

import "testing"

func TestCachedSignalsCoverRejectsNewPID(t *testing.T) {
	info := map[int]processFacts{123: {ppid: 1, command: "worker"}}
	covered := map[int]struct{}{123: {}}

	if !cachedSignalsCover([]int{123}, covered, info) {
		t.Fatal("cachedSignalsCover rejected a PID already in the cache")
	}
	if cachedSignalsCover([]int{123, 456}, covered, info) {
		t.Fatal("cachedSignalsCover reused metadata for a PID not in the cache")
	}
}

// nameCase is one command line and the name the display layer derives from it.
// parent and cwd are optional context: a worker's parent names the service it
// belongs to, and the working directory separates two checkouts that run the
// same binary.
type nameCase struct {
	label string
	cmd   string
	// parent and cwd are empty when the command line alone decides the name.
	parent string
	cwd    string
	want   string
}

func checkNames(t *testing.T, cases []nameCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := resolveProcessName(tc.cmd, tc.parent, tc.cwd); got != tc.want {
				t.Errorf("resolveProcessName(%q, parent %q, cwd %q) = %q, want %q", tc.cmd, tc.parent, tc.cwd, got, tc.want)
			}
		})
	}
}

func TestResolveProcessNameAppBundles(t *testing.T) {
	checkNames(t, []nameCase{
		{label: "app bundle", cmd: "/Applications/Visual Studio Code.app/Contents/MacOS/Electron --type=renderer", want: "Visual Studio Code"},
	})
}

func TestResolveProcessNameBuildOutput(t *testing.T) {
	checkNames(t, []nameCase{
		{label: "go run with cwd", cmd: "/Users/me/projects/example-org-api/tmp/main", cwd: "/Users/me/projects/example-org-api", want: "example-org-api/main"},
		{label: "go run without cwd", cmd: "/Users/me/projects/example-org-api/tmp/main", want: "main"},
		{label: "noise directory", cmd: "/Users/me/projects/myapp/build/server", cwd: "/Users/me/projects/myapp/build", want: "myapp/server"},
		{label: "cargo release directory", cmd: "/projects/myapp/target/release/myapp-server", cwd: "/projects/myapp/target/release", want: "myapp/myapp-server"},
	})
}

func TestResolveProcessNamePython(t *testing.T) {
	checkNames(t, []nameCase{
		{label: "spawn worker names its parent", cmd: "/usr/bin/python -c from multiprocessing.spawn import spawn_main; spawn_main(...)", parent: "/Users/me/.venv/bin/python /Users/me/.venv/bin/uvicorn server:app --reload --port 8001", want: "uvicorn"},
		{label: "spawn worker without parent", cmd: "python -c from foo import bar", want: "python"},
		{label: "script file", cmd: "/usr/bin/python /home/me/app/server.py --port 8000", want: "server.py"},
		{label: "module", cmd: "python -m uvicorn server:app", want: "uvicorn"},
		{label: "dotted module", cmd: "python -m my.app.main", want: "my.app.main"},
	})
}

func TestResolveProcessNameJavaScript(t *testing.T) {
	checkNames(t, []nameCase{
		{label: "node script", cmd: "/usr/local/bin/node /app/server.js", want: "server.js"},
		{label: "npx runner", cmd: "npx next dev", want: "next"},
		{label: "uv runner", cmd: "uv run uvicorn server:app --port 8001", want: "uvicorn"},
		{label: "poetry runner", cmd: "poetry run gunicorn app:application", want: "gunicorn"},
	})
}

func TestResolveProcessNameJava(t *testing.T) {
	checkNames(t, []nameCase{
		{label: "short classpath option", cmd: "/usr/bin/java -cp dep1.jar:dep2.jar:dep3.jar com.myapp.SomeClass", want: "com.myapp.SomeClass"},
		{label: "long classpath option", cmd: "java -classpath /path/to/dep1.jar:/path/to/dep2.jar com.example.Main", want: "com.example.Main"},
		{label: "jar file", cmd: "java -jar /path/to/app.jar", want: "app.jar"},
		{label: "no class at all", cmd: "java", want: "java"},
		{label: "double-dash classpath", cmd: "java --class-path dep1.jar:dep2.jar com.example.Main", want: "com.example.Main"},
		{label: "JVM flags before classpath", cmd: "java -Xmx1g -Dfoo=bar -cp app.jar com.example.Main", want: "com.example.Main"},
		{label: "trailing program arguments", cmd: "java -cp app.jar com.example.Main --port 8080", want: "com.example.Main"},
		{label: "bare main class", cmd: "java com.example.Main", want: "com.example.Main"},
		{label: "classpath option without value", cmd: "java -cp", want: "java"},
		{label: "short module option", cmd: "java -p mods -m com.foo/com.foo.Main", want: "com.foo.Main"},
		{label: "long module option", cmd: "java --module-path mods --module com.foo/com.foo.Main", want: "com.foo.Main"},
		{label: "module option with equals", cmd: "java --module-path mods --module=com.foo/com.foo.Main", want: "com.foo.Main"},
		{label: "module without class", cmd: "java -m com.foo", want: "com.foo"},
	})
}

func TestResolveProcessNameUsesCwdOnlyAsSuffix(t *testing.T) {
	checkNames(t, []nameCase{
		{label: "already carries the cwd name", cmd: "/usr/bin/myapp", cwd: "/projects/myapp", want: "myapp"},
		{label: "system home root is ignored", cmd: "/usr/local/bin/redis-server", cwd: "/Users", want: "redis-server"},
	})
}

func TestResolveProcessNameRealHostCaptures(t *testing.T) {
	// Captured from a real host on 2026-10-01, paths made generic. These are the
	// names a developer actually saw, not a synthetic approximation.
	checkNames(t, []nameCase{
		{label: "app bundle ignores a home cwd", cmd: "/Applications/Warp.app/Contents/MacOS/stable", cwd: "/Users/dev", want: "Warp"},
		{label: "app bundle ignores a support-data cwd", cmd: "/Applications/QQ.app/Contents/MacOS/QQ", cwd: "/Users/dev/Library/Application Support/QQ/Data", want: "QQ"},
		{label: "java jar prepends the project", cmd: "java -Xms512m -Xmx1280m -jar target/example-project.jar --server.port=8089", cwd: "/Users/dev/code/example-project", want: "example-project/example-project.jar"},
		{label: "vite binary from the frontend directory", cmd: "node --max-old-space-size=8192 ./node_modules/vite/bin/vite.js", cwd: "/Users/dev/code/example-project-web", want: "example-project-web/vite.js"},
		{label: "homebrew service keeps its formula", cmd: "/opt/homebrew/opt/redis/bin/redis-server 127.0.0.1:6379", cwd: "/opt/homebrew/var/db/redis", want: "redis/redis-server"},
		{label: "http.server names the module", cmd: "python3 -m http.server 8000", cwd: "/Users/dev/code/site", want: "site/http.server"},
		{label: "nested app helper names the app", cmd: "/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin) --type=utility", cwd: "/Users/dev", want: "Visual Studio Code"},
		{label: "tool in app resources names the app", cmd: "/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/bin/sandbox-cli --prewarm", cwd: "/Users/dev/WorkBuddy/2026-09-30-13-15-02", want: "WorkBuddy"},
		// The JDK ships as an .app too; java running from it is still java, and
		// the main class is the name that identifies the service.
		{label: "bundled interpreter is not the product", cmd: "/opt/elasticsearch/jdk.app/Contents/Home/bin/java -Xshare:auto -Des.path.home=/opt/elasticsearch -cp /opt/elasticsearch/lib/* org.elasticsearch.bootstrap.Elasticsearch", cwd: "/opt/elasticsearch", want: "elasticsearch/org.elasticsearch.bootstrap.Elasticsearch"},
		// RabbitMQ runs as the Erlang VM; the name that means anything is the
		// release script its shell parent was started with.
		{label: "erlang vm names its release script", cmd: "/opt/homebrew/Cellar/erlang@28/28.5.0.5/lib/erlang/erts-16.4.0.5/bin/beam.smp -W w -MBas ageffcbf", parent: "/bin/sh /opt/homebrew/Cellar/rabbitmq/4.3.5/libexec/rabbitmq-server", cwd: "/", want: "rabbitmq-server"},
		{label: "shell wrapper names the script", cmd: "/bin/sh /opt/services/payments/run-server.sh --port 9000", cwd: "/opt/services/payments", want: "payments/run-server.sh"},
	})
}
