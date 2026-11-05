package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

type container struct {
	name           string
	image          string
	portMappings   []portMapping
	composeService string
	composeProject string
	// composeWorkingDir is the directory `docker compose` was run from
	// (label com.docker.compose.project.working_dir). The group resolver uses
	// it to merge a Compose project into the git checkout it belongs to.
	composeWorkingDir string
}

type portMapping struct {
	hostPort      int
	containerPort int
}

// EnrichPorts queries Docker for running containers and enriches any ports
// that match Docker-published host ports. Fails silently if Docker is unavailable.
//
// It asks Docker inline, bounded by CLITimeout, which suits a one-shot CLI
// command. The daemon uses a Watcher instead, so no scan waits on Docker.
func EnrichPorts(pp []ports.ListeningPort) {
	containers, err := listContainers()
	if err != nil {
		return
	}
	enrichFrom(pp, containers)
}

// publishedPorts is the set of host ports these containers publish.
func publishedPorts(containers []container) map[int]bool {
	published := map[int]bool{}
	for _, running := range containers {
		for _, mapping := range running.portMappings {
			published[mapping.hostPort] = true
		}
	}
	return published
}

// enrichFrom marks every port a container publishes as Docker's and copies the
// container's name, image and compose labels onto it. It only reads
// containers, so a Watcher can hand it the cached list without copying.
//
// A port the list claims but some other process holds is left alone: see
// heldByForwarder.
func enrichFrom(pp []ports.ListeningPort, containers []container) {
	if len(containers) == 0 {
		return
	}

	// One host port can have several listening rows (one per bind address), and
	// all of them belong to the same container, so the lookup is keyed by port
	// number rather than by the whole row. The container and the mapping that
	// named it are kept together: a port published twice cannot disagree with
	// itself, and two parallel maps could.
	owner := make(map[int]portOwner, len(containers))
	for i := range containers {
		for _, mapping := range containers[i].portMappings {
			owner[mapping.hostPort] = portOwner{container: &containers[i], mapping: mapping}
		}
	}

	for i := range pp {
		found, claimed := owner[pp[i].Port]
		if !claimed {
			continue
		}
		if !heldByForwarder(&pp[i]) {
			// The list says a container publishes this port, but a native
			// process holds it. The list can be stale — the watcher keeps
			// serving its last good one while Docker is not answering — and
			// stamping the row would make `ports.kill` run `docker stop`
			// against the wrong thing instead of signalling the real owner
			// (issue #95).
			continue
		}
		row := &pp[i]
		row.Type = ports.PortTypeDocker
		row.DockerContainer = found.container.name
		row.DockerImage = found.container.image
		row.DockerComposeService = found.container.composeService
		row.DockerComposeProject = found.container.composeProject
		row.DockerComposeWorkingDir = found.container.composeWorkingDir
		row.DockerContainerPort = found.mapping.containerPort
	}
}

// portOwner is which container publishes a host port, and through which
// mapping. The two travel together because a row needs both.
type portOwner struct {
	container *container
	mapping   portMapping
}

// ContainerStats holds per-container resource usage.
type ContainerStats struct {
	CPUPercent float64
	MemoryRSS  int64
	PIDs       int
	State      string
	Uptime     string
}

// dockerSocket is the default Docker Engine API socket path.
const dockerSocket = "/var/run/docker.sock"

// apiStatsResponse matches the Docker Engine /containers/{id}/stats response.
type apiStatsResponse struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	PrecpuStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
	} `json:"memory_stats"`
	PidsStats struct {
		Current int `json:"current"`
	} `json:"pids_stats"`
}

// apiInspectState matches the relevant fields from /containers/{id}/json.
type apiInspectState struct {
	State struct {
		Status    string `json:"Status"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
}

// AllContainerStatsAsEntries returns stats for all containers as ports.DockerStatsEntry map.
func AllContainerStatsAsEntries() map[string]*ports.DockerStatsEntry {
	return statsEntries(AllContainerStats())
}

// statsEntries converts per-container stats to the shape ports.EnrichStats
// takes. No stats at all is nil rather than an empty map, so a caller can tell
// "nothing sampled" from "sampled, nothing running".
func statsEntries(allStats map[string]*ContainerStats) map[string]*ports.DockerStatsEntry {
	if len(allStats) == 0 {
		return nil
	}
	entries := make(map[string]*ports.DockerStatsEntry, len(allStats))
	for name, measured := range allStats {
		entries[name] = &ports.DockerStatsEntry{
			CPUPercent: measured.CPUPercent,
			MemoryRSS:  measured.MemoryRSS,
			PIDs:       measured.PIDs,
			State:      measured.State,
			Uptime:     measured.Uptime,
		}
	}
	return entries
}

// AllContainerStats samples every running container, listing them first.
func AllContainerStats() map[string]*ContainerStats {
	listed, err := listContainers()
	if err != nil {
		return map[string]*ContainerStats{}
	}
	return statsFor(listed)
}

// statsFor samples the given containers.
//
// On Linux the Engine API over the local socket is used, one request per
// container in parallel: /stats?stream=false makes the daemon collect a CPU
// sample for about a second before answering, so serialising the calls would
// cost a second per container. Everything else — Windows, or a host with no
// socket (Docker Desktop forwards through it only for some configurations) —
// falls back to the `docker stats` CLI, which does the same sampling itself.
func statsFor(containers []container) map[string]*ContainerStats {
	sampled := make(map[string]*ContainerStats)
	if len(containers) == 0 {
		return sampled
	}
	if !hasEngineSocket() {
		return allContainerStatsCLI()
	}
	for name, measured := range sampleViaAPI(containers) {
		sampled[name] = measured
	}
	return sampled
}

// hasEngineSocket reports whether the Engine API is reachable over the local
// socket, which is the only way this path talks to Docker.
func hasEngineSocket() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	_, err := os.Stat(dockerSocket)
	return err == nil
}

// sampleViaAPI asks the Engine API about every container at once and returns
// the ones that answered. A container whose request failed is simply absent.
func sampleViaAPI(containers []container) map[string]*ContainerStats {
	type sample struct {
		name     string
		measured *ContainerStats
	}
	answered := make(chan sample, len(containers))
	for _, running := range containers {
		go func(name string) {
			answered <- sample{name: name, measured: fetchContainerStatsAPI(name)}
		}(running.name)
	}

	sampled := make(map[string]*ContainerStats, len(containers))
	for range containers {
		got := <-answered
		if got.measured != nil {
			sampled[got.name] = got.measured
		}
	}
	return sampled
}

// fetchContainerStatsAPI fetches stats for a single container via the Docker socket API.
func fetchContainerStatsAPI(name string) *ContainerStats {
	conn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		return nil
	}

	stats := &ContainerStats{}

	statsReq := fmt.Sprintf("GET /containers/%s/stats?stream=false HTTP/1.0\r\nHost: localhost\r\n\r\n", name)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write([]byte(statsReq))
	if err != nil {
		conn.Close()
		return nil
	}

	statsBody, err := readHTTPBody(conn)
	conn.Close()
	if err != nil {
		return nil
	}

	var apiStats apiStatsResponse
	if err := json.Unmarshal(statsBody, &apiStats); err != nil {
		return nil
	}

	cpuDelta := apiStats.CPUStats.CPUUsage.TotalUsage - apiStats.PrecpuStats.CPUUsage.TotalUsage
	sysDelta := apiStats.CPUStats.SystemCPUUsage - apiStats.PrecpuStats.SystemCPUUsage
	ncpu := apiStats.CPUStats.OnlineCPUs
	if ncpu == 0 {
		ncpu = 1
	}
	if sysDelta > 0 {
		stats.CPUPercent = float64(cpuDelta) / float64(sysDelta) * float64(ncpu) * 100.0
	}

	stats.MemoryRSS = int64(apiStats.MemoryStats.Usage)
	stats.PIDs = apiStats.PidsStats.Current

	// Fetch /json for state and start time
	conn2, err := net.Dial("unix", dockerSocket)
	if err != nil {
		return stats
	}
	defer conn2.Close()

	inspectReq := fmt.Sprintf("GET /containers/%s/json HTTP/1.0\r\nHost: localhost\r\n\r\n", name)
	conn2.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = conn2.Write([]byte(inspectReq))
	if err != nil {
		return stats
	}

	inspectBody, err := readHTTPBody(conn2)
	if err != nil {
		return stats
	}

	var inspect apiInspectState
	if err := json.Unmarshal(inspectBody, &inspect); err != nil {
		return stats
	}
	stats.State = inspect.State.Status
	stats.Uptime = computeContainerUptime(inspect.State.StartedAt)

	return stats
}

// readHTTPBody reads an HTTP/1.0 response from a connection and returns the body.
func readHTTPBody(conn net.Conn) ([]byte, error) {
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	return io.ReadAll(reader)
}

// allContainerStatsCLI is the fallback using `docker stats` CLI.
func allContainerStatsCLI() map[string]*ContainerStats {
	result := make(map[string]*ContainerStats)

	out, err := dockerOutput("stats", "--no-stream", "--format",
		"{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.PIDs}}")
	if err != nil {
		return result
	}

	scanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(string(out))))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "\t", 4)
		if len(parts) < 4 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		stats := &ContainerStats{}

		cpuStr, _ := strings.CutSuffix(strings.TrimSpace(parts[1]), "%")
		if v, err := strconv.ParseFloat(cpuStr, 64); err == nil {
			stats.CPUPercent = v
		}

		memParts := strings.SplitN(strings.TrimSpace(parts[2]), " / ", 2)
		if len(memParts) >= 1 {
			stats.MemoryRSS = parseMemString(memParts[0])
		}

		if v, err := strconv.Atoi(strings.TrimSpace(parts[3])); err == nil {
			stats.PIDs = v
		}

		result[name] = stats
	}

	// Fetch state/uptime via inspect
	names := make([]string, 0, len(result))
	for name := range result {
		names = append(names, name)
	}
	if len(names) > 0 {
		args := append([]string{"inspect", "--format", "{{.Name}}\t{{.State.Status}}\t{{.State.StartedAt}}"}, names...)
		inspectOut, err := dockerOutput(args...)
		if err == nil {
			inspectScanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(string(inspectOut))))
			for inspectScanner.Scan() {
				parts := strings.SplitN(inspectScanner.Text(), "\t", 3)
				if len(parts) < 3 {
					continue
				}
				name := strings.TrimPrefix(strings.TrimSpace(parts[0]), "/")
				if stats, ok := result[name]; ok {
					stats.State = parts[1]
					stats.Uptime = computeContainerUptime(parts[2])
				}
			}
		}
	}

	return result
}

// parseMemString parses Docker memory strings like "357.7MiB", "1.5GiB", "512KiB".
func parseMemString(s string) int64 {
	s = strings.TrimSpace(s)
	multiplier := int64(1)
	if after, found := strings.CutSuffix(s, "GiB"); found {
		multiplier = 1 << 30
		s = after
	} else if after, found := strings.CutSuffix(s, "MiB"); found {
		multiplier = 1 << 20
		s = after
	} else if after, found := strings.CutSuffix(s, "KiB"); found {
		multiplier = 1 << 10
		s = after
	} else if after, found := strings.CutSuffix(s, "B"); found {
		s = after
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(v * float64(multiplier))
}

// computeContainerUptime parses a Docker ISO 8601 timestamp and returns a human-readable duration.
func computeContainerUptime(startedAt string) string {
	layouts := []string{
		"2006-01-02T15:04:05.999999999Z",
		"2006-01-02T15:04:05.999999999Z07:00",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, startedAt); err == nil {
			d := time.Since(t)
			return formatContainerDuration(d)
		}
	}
	return ""
}

func formatContainerDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	return fmt.Sprintf("%dd%dh", days, h)
}

// StopContainer stops a Docker container by name using `docker stop`.
func StopContainer(name string) error {
	cmd, stop := timedCommand("stop", name)
	defer stop()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", name, err)
	}
	return nil
}

// psFormat is the `docker ps --format` template parsePS reads.
const psFormat = "{{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Label \"com.docker.compose.service\"}}" +
	"\t{{.Label \"com.docker.compose.project\"}}\t{{.Label \"com.docker.compose.project.working_dir\"}}"

// listContainers runs `docker ps` under CLITimeout and parses the output.
func listContainers() ([]container, error) {
	ctx, cancel := context.WithTimeout(context.Background(), CLITimeout)
	defer cancel()
	return listContainersCtx(ctx)
}

// listContainersCtx runs `docker ps` bound to ctx and parses the output.
func listContainersCtx(ctx context.Context) ([]container, error) {
	out, err := dockerCommand(ctx, "ps", "--format", psFormat).Output()
	if err != nil {
		return nil, err
	}
	return parsePS(out), nil
}

// parsePS parses `docker ps --format psFormat` output, one container a line.
func parsePS(out []byte) []container {
	var containers []container
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}

		c := container{
			name:              parts[0],
			image:             parts[1],
			portMappings:      parsePorts(parts[2]),
			composeService:    parts[3],
			composeProject:    parts[4],
			composeWorkingDir: parts[5],
		}
		containers = append(containers, c)
	}

	return containers
}

// parsePorts parses Docker port strings like "0.0.0.0:3000->80/tcp, 0.0.0.0:3001->443/tcp".
func parsePorts(raw string) []portMapping {
	if raw == "" {
		return nil
	}

	var mappings []portMapping
	for _, entry := range strings.Split(raw, ", ") {
		// Format: 0.0.0.0:3000->80/tcp or :::3000->80/tcp
		arrowIdx := strings.Index(entry, "->")
		if arrowIdx < 0 {
			continue
		}

		hostPart := entry[:arrowIdx]
		containerPart := entry[arrowIdx+2:]

		// Extract host port (after last colon)
		colonIdx := strings.LastIndex(hostPart, ":")
		if colonIdx < 0 {
			continue
		}
		hostPort, err := strconv.Atoi(hostPart[colonIdx+1:])
		if err != nil {
			continue
		}

		// Extract container port (before /tcp or /udp)
		slashIdx := strings.Index(containerPart, "/")
		cpStr := containerPart
		if slashIdx >= 0 {
			cpStr = containerPart[:slashIdx]
		}
		containerPort, err := strconv.Atoi(cpStr)
		if err != nil {
			continue
		}

		mappings = append(mappings, portMapping{
			hostPort:      hostPort,
			containerPort: containerPort,
		})
	}

	return mappings
}
