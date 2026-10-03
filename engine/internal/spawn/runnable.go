package spawn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckRunnable reports, in words a person can act on, why the program a
// service's `cmd:` names cannot be started.
//
// exec's own error for this is `exec: "docker": executable file not found in
// $PATH`. That is accurate and useless to someone who *did* install docker: the
// two cases behind it are "a PATH that does not have it" and "a link on PATH
// that points at something gone" (this machine had seven of those), and only
// the second is a thing they can fix by looking.
//
// The PATH consulted is the one the service will actually see — the daemon's,
// not the caller's — because that is the one that decides whether the command
// runs.
func CheckRunnable(argv0 string, env []string) error {
	if argv0 == "" {
		return errors.New("the service has no command to run")
	}
	if strings.ContainsRune(argv0, os.PathSeparator) {
		// A path is the caller's own; only existence can be judged from here.
		if _, err := os.Stat(argv0); err != nil {
			return fmt.Errorf("%s cannot be run: %w", argv0, err)
		}
		return nil
	}

	pathEnv := envValue(env, "PATH")
	if pathEnv == "" {
		pathEnv = os.Getenv("PATH")
	}
	found, runnable := findOnPath(argv0, pathEnv)
	switch {
	case runnable:
		return nil
	case found != "":
		return fmt.Errorf("%s is on PATH at %s but cannot be run — a broken symlink?", argv0, found)
	default:
		return fmt.Errorf("%s is not on PATH", argv0)
	}
}

// CheckRunnableInDir is CheckRunnable for a command that will be launched
// with cwd. Relative paths such as ./mvnw are resolved against that cwd, just
// as exec.Command does; checking them against the daemon's own cwd would make
// perfectly valid project wrappers fail before they ever start.
func CheckRunnableInDir(argv0, cwd string, env []string) error {
	if cwd != "" && !filepath.IsAbs(argv0) && strings.ContainsAny(argv0, `/\\`) {
		argv0 = filepath.Join(cwd, argv0)
	}
	return CheckRunnable(argv0, env)
}

// findOnPath walks PATH the way exec does, but tells "a file that cannot be run"
// from "no file at all" — the difference between "I have docker installed" and
// "I do not".
func findOnPath(name, pathEnv string) (found string, runnable bool) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		info, err := os.Stat(p) // Stat follows links: a broken one errors
		if err != nil {
			if _, linkErr := os.Lstat(p); linkErr == nil {
				return p, false
			}
			continue
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return p, true
		}
		return p, false
	}
	return "", false
}

// envValue reads one variable out of a []string{"K=V"} environment, last
// definition winning, which is what exec does with a duplicate.
func envValue(env []string, key string) string {
	prefix := key + "="
	out := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			out = v
		}
	}
	return out
}
