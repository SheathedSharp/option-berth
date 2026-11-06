package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeDocker puts a POSIX shell script named docker first on PATH, so the
// probe is exercised without the machine's real Docker.
func fakeDocker(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell fixture")
	}
	dir := t.TempDir()
	script := write(t, filepath.Join(dir, "docker"), "#!/bin/sh\n"+body+"\n")
	mustChmod(t, script, 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDockerProbeReadsTheServerVersion(t *testing.T) {
	fakeDocker(t, "printf '27.0.1\\n'")
	version, err := DockerProbe(context.Background())
	if err != nil || version != "27.0.1" {
		t.Fatalf("DockerProbe = %q, %v; want 27.0.1", version, err)
	}
}

func TestDockerProbeReportsTheFirstStderrLine(t *testing.T) {
	fakeDocker(t, "printf 'Cannot connect to the Docker daemon\\nsecond line\\n' >&2\nexit 1")
	_, err := DockerProbe(context.Background())
	if err == nil || err.Error() != "Cannot connect to the Docker daemon" {
		t.Fatalf("DockerProbe error = %v, want the first stderr line", err)
	}
}

func TestDockerProbeHonoursTheContextDeadline(t *testing.T) {
	fakeDocker(t, "exec sleep 5")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := DockerProbe(ctx)
	if !errors.Is(err, ErrDockerNotAnswering) {
		t.Fatalf("DockerProbe error = %v, want ErrDockerNotAnswering", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("DockerProbe took %s, want it to stop with the context", elapsed)
	}
}
