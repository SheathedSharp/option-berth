package runtimesvc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// defaultLogLines is the backlog a caller gets when it does not ask for one.
const defaultLogLines = 100

// logSource is one service's output: where it can be read from, and how.
//
// Two kinds exist and they are genuinely different. A native service's output is
// a file on this machine, so it is read here. A container's output lives inside
// Docker, so the daemon asks Docker for it — that is a fact only the container
// runtime holds, not a program the engine could avoid needing.
type logSource interface {
	// Label names the source for the client.
	Label() string
	// Tail returns the last lines of output, oldest first.
	Tail(ctx context.Context, lines int) ([]string, error)
	// Follow streams the backlog and then every line after it until ctx ends.
	Follow(ctx context.Context, lines int, emit func(string) error) error
}

// fileLog is a native service's output read from the files the process holds.
type fileLog struct {
	paths  []string
	labels []string
}

func (source fileLog) Label() string { return strings.Join(source.labels, ", ") }

func (source fileLog) Tail(_ context.Context, lines int) ([]string, error) {
	var out []string
	for _, path := range source.paths {
		lines, err := tailFile(path, lines)
		if err != nil {
			return nil, rpc.NewError(rpc.CodeNotFound,
				fmt.Sprintf("reading %s: %v", path, err), "")
		}
		out = append(out, lines...)
	}
	return out, nil
}

func (source fileLog) Follow(ctx context.Context, lines int, emit func(string) error) error {
	return followFiles(ctx, source.paths, lines, emit)
}

// containerLog asks Docker for one container's output.
type containerLog struct{ container string }

func (source containerLog) Label() string { return "docker:" + source.container }

func (source containerLog) Tail(ctx context.Context, lines int) ([]string, error) {
	return runDockerLog(ctx, lines, false, source.container)
}

func (source containerLog) Follow(ctx context.Context, lines int, emit func(string) error) error {
	argv := []string{"docker", "logs", "--tail", strconv.Itoa(lines), "-f", source.container}
	return streamCommand(ctx, argv, emit)
}

// resolveLogSource decides where a port's output can be read from. The ladder is
// the one `oberth logs` has always walked: a container's own log, then the files
// the process holds open, then the platform's descriptor table.
//
// There is no last resort on purpose: a process that writes to a terminal or a
// pipe has no log file, and handing over the machine's own log would be
// presenting something else as this service's output.
func resolveLogSource(row state.Port, lines int) (logSource, error) {
	if row.Docker != nil && row.Docker.Container != "" {
		return containerLog{container: row.Docker.Container}, nil
	}
	if sources := ports.FindLogSources(row.PID); len(sources) > 0 {
		return fileLog{paths: logPathsFor(sources), labels: logLabelsFor(sources)}, nil
	}
	if paths := descriptorPaths(row.PID); len(paths) > 0 {
		return fileLog{paths: paths, labels: paths}, nil
	}
	return nil, rpc.NewError(rpc.CodeNotFound,
		fmt.Sprintf("no log file for pid %d", row.PID),
		"its output does not go to a file; option-berth collects the logs of the services it starts")
}

// descriptorPaths is the Linux descriptor table's view of a process's output,
// for a process whose open files the collector could not resolve.
func descriptorPaths(pid int) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	var paths []string
	for _, fd := range []string{"1", "2"} {
		path := fmt.Sprintf("/proc/%d/fd/%s", pid, fd)
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		}
	}
	return paths
}

// handlePortsLogs is unary with follow:false ({source, lines, truncated}) and
// streaming with follow:true, where the backlog and everything after it arrive
// as stream.chunk notifications.
func handlePortsLogs(ctx context.Context, req *Request) (any, error) {
	var p rpc.PortsLogsParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	requested := p.Lines
	if requested <= 0 {
		requested = defaultLogLines
	}

	listening, err := readPorts(req.Runtime, scanner.Include{})
	if err != nil {
		return nil, err
	}
	match, err := resolvePort(listening, p.Selector)
	if err != nil {
		return nil, err
	}
	source, err := resolveLogSource(match, requested)
	if err != nil {
		return nil, err
	}

	if !p.Follow {
		out, err := source.Tail(ctx, requested)
		if err != nil {
			return nil, err
		}
		return rpc.PortsLogsResult{
			Source:    source.Label(),
			Lines:     out,
			Truncated: len(out) >= requested,
		}, nil
	}

	initial := rpc.PortsLogsResult{Source: source.Label(), Lines: []string{}}
	return StartStream(ctx, req, initial, func(ctx context.Context, s *Stream) (any, error) {
		return nil, source.Follow(ctx, requested, func(line string) error {
			return s.Send(rpc.PortsLogsChunk{Source: source.Label(), Line: line})
		})
	})
}

// runDockerLog reads a bounded amount of a container's output in one call.
func runDockerLog(ctx context.Context, lines int, follow bool, container string) ([]string, error) {
	var out []string
	err := streamCommand(ctx, []string{"docker", "logs", "--tail", strconv.Itoa(lines), container}, func(line string) error {
		out = append(out, line)
		return nil
	})
	return out, err
}

// streamCommand runs one external command and hands each of its output lines to
// emit. This is the only place the daemon reads a log by running a program: it
// exists for a container's output, which only the container runtime can supply.
//
// Killing the child is the context's job: exec.CommandContext does it on
// cancellation.
func streamCommand(ctx context.Context, argv []string, emit func(string) error) error {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return rpc.NewError(rpc.CodeUnsupported, argv[0]+" is not on PATH", "")
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	pipe, err := command.StdoutPipe()
	if err != nil {
		return rpc.NewError(rpc.CodeInternal, "opening the log pipe: "+err.Error(), "")
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		return rpc.NewError(rpc.CodeInternal, fmt.Sprintf("starting %s: %v", argv[0], err), "")
	}
	defer func() { _ = command.Wait() }()

	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if err := emit(scanner.Text()); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil && !errIsClosed(err) {
		return err
	}
	return ctx.Err()
}

func errIsClosed(err error) bool {
	return err == io.ErrClosedPipe || strings.Contains(err.Error(), "file already closed")
}
