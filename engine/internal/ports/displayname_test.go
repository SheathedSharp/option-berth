package ports

import "testing"

func TestCachedSignalsCoverRejectsNewPID(t *testing.T) {
	info := map[int]pidEntry{123: {ppid: 1, cmd: "worker"}}
	covered := map[int]struct{}{123: {}}

	if !cachedSignalsCover([]int{123}, covered, info) {
		t.Fatal("cachedSignalsCover rejected a PID already in the cache")
	}
	if cachedSignalsCover([]int{123, 456}, covered, info) {
		t.Fatal("cachedSignalsCover reused metadata for a PID not in the cache")
	}
}

func checkResolved(t *testing.T, cmd, parent, cwd, want string) {
	t.Helper()
	if got := resolveProcessName(cmd, parent, cwd); got != want {
		t.Errorf("\ncmd:       %q\nparentCmd: %q\ncwd:       %q\ngot:  %q\nwant: %q",
			cmd, parent, cwd, got, want)
	}
}

func TestResolveBundlesAndBinaryPaths(t *testing.T) {
	checkResolved(t,
		"/Applications/Visual Studio Code.app/Contents/MacOS/Electron --type=renderer",
		"", "", "Visual Studio Code")
	checkResolved(t,
		"/Applications/Warp.app/Contents/MacOS/stable",
		"", "/Users/dev", "Warp")
	checkResolved(t,
		"/Applications/QQ.app/Contents/MacOS/QQ",
		"", "/Users/dev/Library/Application Support/QQ/Data", "QQ")
	checkResolved(t,
		"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin) --type=utility",
		"", "/Users/dev", "Visual Studio Code")
	checkResolved(t,
		"/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/bin/sandbox-cli --prewarm",
		"", "/Users/dev/WorkBuddy/2026-09-30-13-15-02", "WorkBuddy")

	checkResolved(t,
		"/Users/me/projects/acme-api/tmp/main",
		"", "/Users/me/projects/acme-api", "acme-api/main")
	checkResolved(t,
		"/Users/me/projects/acme-api/tmp/main",
		"", "", "main")
	checkResolved(t,
		"/Users/me/projects/shop/build/server",
		"", "/Users/me/projects/shop/build", "shop/server")
	checkResolved(t,
		"/projects/shop/target/release/shop-server",
		"", "/projects/shop/target/release", "shop/shop-server")
	checkResolved(t,
		"/opt/homebrew/opt/redis/bin/redis-server 127.0.0.1:6379",
		"", "/opt/homebrew/var/db/redis", "redis/redis-server")
	checkResolved(t,
		"/usr/bin/myapp",
		"", "/projects/myapp", "myapp")
	checkResolved(t,
		"/usr/local/bin/redis-server",
		"", "/Users", "redis-server")
	checkResolved(t,
		"/bin/sh /opt/services/payments/run-server.sh --port 9000",
		"", "/opt/services/payments", "payments/run-server.sh")
}

func TestResolveInterpreterCommands(t *testing.T) {
	checkResolved(t,
		"/usr/bin/python -c from multiprocessing.spawn import spawn_main; spawn_main(...)",
		"/Users/me/.venv/bin/python /Users/me/.venv/bin/uvicorn server:app --reload --port 8001",
		"", "uvicorn")
	checkResolved(t, "python -c from foo import bar", "", "", "python")

	checkResolved(t, "uv run uvicorn server:app --port 8001", "", "", "uvicorn")
	checkResolved(t, "poetry run gunicorn app:application", "", "", "gunicorn")
	checkResolved(t, "npx next dev", "", "", "next")

	checkResolved(t, "/usr/bin/python /home/me/app/server.py --port 8000", "", "", "server.py")
	checkResolved(t, "python -m uvicorn server:app", "", "", "uvicorn")
	checkResolved(t, "python -m my.app.main", "", "", "my.app.main")
	checkResolved(t, "python3 -m http.server 8000", "", "/Users/dev/code/site", "site/http.server")

	checkResolved(t, "/usr/local/bin/node /app/server.js", "", "", "server.js")
	checkResolved(t,
		"node --max-old-space-size=8192 ./node_modules/vite/bin/vite.js",
		"", "/Users/dev/code/example-web", "example-web/vite.js")

	checkResolved(t,
		"/opt/homebrew/Cellar/erlang@28/28.5.0.5/lib/erlang/erts-16.4.0.5/bin/beam.smp -W w -MBas ageffcbf",
		"/bin/sh /opt/homebrew/Cellar/rabbitmq/4.3.5/libexec/rabbitmq-server",
		"/", "rabbitmq-server")

	checkResolved(t,
		"/opt/elasticsearch/jdk.app/Contents/Home/bin/java -Xshare:auto -Des.path.home=/opt/elasticsearch -cp /opt/elasticsearch/lib/* org.elasticsearch.bootstrap.Elasticsearch",
		"", "/opt/elasticsearch",
		"elasticsearch/org.elasticsearch.bootstrap.Elasticsearch")
}

func TestResolveJavaCommands(t *testing.T) {
	checkResolved(t,
		"/usr/bin/java -cp dep1.jar:dep2.jar:dep3.jar com.myapp.SomeClass",
		"", "", "com.myapp.SomeClass")
	checkResolved(t,
		"java -classpath /path/to/dep1.jar:/path/to/dep2.jar com.example.Main",
		"", "", "com.example.Main")
	checkResolved(t, "java -jar /path/to/app.jar", "", "", "app.jar")
	checkResolved(t, "java", "", "", "java")
	checkResolved(t, "java --class-path dep1.jar:dep2.jar com.example.Main", "", "", "com.example.Main")
	checkResolved(t, "java -Xmx1g -Dfoo=bar -cp app.jar com.example.Main", "", "", "com.example.Main")
	checkResolved(t, "java -cp app.jar com.example.Main --port 8080", "", "", "com.example.Main")
	checkResolved(t, "java com.example.Main", "", "", "com.example.Main")
	checkResolved(t, "java -cp", "", "", "java")
	checkResolved(t, "java -p mods -m com.foo/com.foo.Main", "", "", "com.foo.Main")
	checkResolved(t, "java --module-path mods --module com.foo/com.foo.Main", "", "", "com.foo.Main")
	checkResolved(t, "java --module-path mods --module=com.foo/com.foo.Main", "", "", "com.foo.Main")
	checkResolved(t, "java -m com.foo", "", "", "com.foo")
	checkResolved(t,
		"java -Xms512m -Xmx1280m -jar target/example-project.jar --server.port=8089",
		"", "/Users/dev/code/example-project", "example-project/example-project.jar")
}
