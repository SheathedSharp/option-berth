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

// Timeouts for the two Engine API calls one container needs. A stats request
// makes the daemon collect a CPU sample, so it is allowed longer than the
// inspect call that only reads metadata.
const (
	statsRequestTimeout   = 5 * time.Second
	inspectRequestTimeout = 2 * time.Second
)

// fetchContainerStatsAPI samples one container over the Docker socket. A nil
// result means the container did not answer, which the caller reports as an
// absent row rather than as a container with zero usage.
func fetchContainerStatsAPI(name string) *ContainerStats {
	body, err := dockerAPIGet("/containers/"+name+"/stats?stream=false", statsRequestTimeout)
	if err != nil {
		return nil
	}
	var payload apiStatsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	stats := measurements(payload)

	// State and start time live on a different endpoint. Losing it costs the
	// row its state and uptime, not its numbers, so the failure is not fatal.
	described, err := dockerAPIGet("/containers/"+name+"/json", inspectRequestTimeout)
	if err != nil {
		return stats
	}
	var inspect apiInspectState
	if err := json.Unmarshal(described, &inspect); err != nil {
		return stats
	}
	stats.State = inspect.State.Status
	stats.Uptime = computeContainerUptime(inspect.State.StartedAt)
	return stats
}

// dockerAPIGet performs one HTTP/1.0 GET against the Docker socket and returns
// the response body.
func dockerAPIGet(endpoint string, timeout time.Duration) ([]byte, error) {
	conn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	request := "GET " + endpoint + " HTTP/1.0\r\nHost: localhost\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		return nil, err
	}
	return readHTTPBody(conn)
}

// measurements turns one /stats response into the numbers this package reports.
//
// CPU is the share of the host's CPU time the container used between the two
// samples the daemon took, scaled by the number of online CPUs so the figure is
// comparable to what `docker stats` prints. A zero system delta means the two
// samples are unusable, and no percentage is reported rather than an infinite
// one.
func measurements(payload apiStatsResponse) *ContainerStats {
	stats := &ContainerStats{}
	cpuDelta := payload.CPUStats.CPUUsage.TotalUsage - payload.PrecpuStats.CPUUsage.TotalUsage
	systemDelta := payload.CPUStats.SystemCPUUsage - payload.PrecpuStats.SystemCPUUsage
	online := payload.CPUStats.OnlineCPUs
	if online == 0 {
		online = 1
	}
	if systemDelta > 0 {
		stats.CPUPercent = float64(cpuDelta) / float64(systemDelta) * float64(online) * 100
	}
	stats.MemoryRSS = int64(payload.MemoryStats.Usage)
	stats.PIDs = payload.PidsStats.Current
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

// statsRowFormat is the `docker stats` template: one tab-separated row per
// container, which is the only shape this code parses.
const statsRowFormat = "{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.PIDs}}"

// inspectRowFormat is the `docker inspect` template for the two fields
// /stats does not carry.
const inspectRowFormat = "{{.Name}}\t{{.State.Status}}\t{{.State.StartedAt}}"

// allContainerStatsCLI samples every container through the `docker stats` CLI.
// It is the fallback for Windows and for hosts where the Engine socket is not
// reachable; the CLI does its own sampling, so one call covers them all.
func allContainerStatsCLI() map[string]*ContainerStats {
	table, err := dockerOutput("stats", "--no-stream", "--format", statsRowFormat)
	if err != nil {
		return map[string]*ContainerStats{}
	}
	sampled := parseStatsTable(string(table))
	applyInspect(sampled)
	return sampled
}

// parseStatsTable reads the rows `docker stats` printed. A row that does not
// have all four columns is skipped rather than reported with zeroes.
func parseStatsTable(table string) map[string]*ContainerStats {
	sampled := make(map[string]*ContainerStats)
	rows := bufio.NewScanner(strings.NewReader(strings.TrimSpace(table)))
	for rows.Scan() {
		fields := strings.SplitN(rows.Text(), "\t", 4)
		if len(fields) < 4 {
			continue
		}
		sampled[strings.TrimSpace(fields[0])] = statsFromRow(fields[1], fields[2], fields[3])
	}
	return sampled
}

// statsFromRow turns the cpu, memory and pids columns into a sample. Each
// column is parsed on its own, so one unreadable value does not discard the
// others.
func statsFromRow(cpuColumn, memoryColumn, pidsColumn string) *ContainerStats {
	stats := &ContainerStats{}

	percent := strings.TrimSuffix(strings.TrimSpace(cpuColumn), "%")
	if percent, err := strconv.ParseFloat(percent, 64); err == nil {
		stats.CPUPercent = percent
	}
	// The memory column reads "357.7MiB / 7.66GiB": the first half is what the
	// container uses, the second is the limit.
	used, _, _ := strings.Cut(strings.TrimSpace(memoryColumn), " / ")
	stats.MemoryRSS = parseMemString(used)

	if pids, err := strconv.Atoi(strings.TrimSpace(pidsColumn)); err == nil {
		stats.PIDs = pids
	}
	return stats
}

// applyInspect fills in state and start time, which `docker stats` does not
// report. A failed inspect leaves the numbers alone rather than dropping the
// rows, so a container still shows its usage without its uptime.
func applyInspect(sampled map[string]*ContainerStats) {
	if len(sampled) == 0 {
		return
	}
	names := make([]string, 0, len(sampled))
	for name := range sampled {
		names = append(names, name)
	}
	described, err := dockerOutput(append([]string{"inspect", "--format", inspectRowFormat}, names...)...)
	if err != nil {
		return
	}

	rows := bufio.NewScanner(strings.NewReader(strings.TrimSpace(string(described))))
	for rows.Scan() {
		fields := strings.SplitN(rows.Text(), "\t", 3)
		if len(fields) < 3 {
			continue
		}
		// inspect prints the name with its leading slash.
		name := strings.TrimPrefix(strings.TrimSpace(fields[0]), "/")
		if stats, listed := sampled[name]; listed {
			stats.State = fields[1]
			stats.Uptime = computeContainerUptime(fields[2])
		}
	}
}

// memoryUnits are the suffixes `docker stats` prints, largest first so a longer
// suffix is tested before the "B" it ends with.
var memoryUnits = []struct {
	suffix string
	scale  int64
}{
	{"GiB", 1 << 30},
	{"MiB", 1 << 20},
	{"KiB", 1 << 10},
	{"B", 1},
}

// parseMemString turns a memory string such as "357.7MiB" or "512KiB" into
// bytes. An unreadable value is zero.
func parseMemString(printed string) int64 {
	remaining := strings.TrimSpace(printed)
	for _, unit := range memoryUnits {
		digits, found := strings.CutSuffix(remaining, unit.suffix)
		if !found {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(digits), 64)
		if err != nil {
			return 0
		}
		return int64(value * float64(unit.scale))
	}
	return 0
}

// dockerStartTimeLayouts are the two shapes Docker prints StartedAt in: a
// UTC instant with nanoseconds, and the same with a numeric offset.
var dockerStartTimeLayouts = []string{
	"2006-01-02T15:04:05.999999999Z",
	"2006-01-02T15:04:05.999999999Z07:00",
}

// computeContainerUptime renders how long ago a container started, from the
// StartedAt Docker reports. An unparseable timestamp yields "" rather than a
// wrong age.
func computeContainerUptime(startedAt string) string {
	for _, layout := range dockerStartTimeLayouts {
		started, err := time.Parse(layout, startedAt)
		if err != nil {
			continue
		}
		return formatContainerDuration(time.Since(started))
	}
	return ""
}

// formatContainerDuration renders a container's age at the coarsest useful
// resolution: seconds, minutes, hours, then days.
func formatContainerDuration(age time.Duration) string {
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm%ds", int(age.Minutes()), int(age.Seconds())%60)
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(age.Hours()), int(age.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(age.Hours())/24, int(age.Hours())%24)
	}
}

// StopContainer stops a container by name through `docker stop`.
func StopContainer(name string) error {
	stop, release := timedCommand("stop", name)
	defer release()
	if err := stop.Run(); err != nil {
		return fmt.Errorf("stopping container %s: %w", name, err)
	}
	return nil
}

// psFormat is the `docker ps --format` template parsePS reads: one
// tab-separated row per container, the compose labels last.
const psFormat = "{{.Names}}\t{{.Image}}\t{{.Ports}}" +
	"\t{{.Label \"com.docker.compose.service\"}}" +
	"\t{{.Label \"com.docker.compose.project\"}}" +
	"\t{{.Label \"com.docker.compose.project.working_dir\"}}"

// listContainers runs `docker ps` under CLITimeout.
func listContainers() ([]container, error) {
	ctx, cancel := context.WithTimeout(context.Background(), CLITimeout)
	defer cancel()
	return listContainersCtx(ctx)
}

// listContainersCtx runs `docker ps` bound to ctx and parses the answer.
func listContainersCtx(ctx context.Context) ([]container, error) {
	printed, err := dockerCommand(ctx, "ps", "--format", psFormat).Output()
	if err != nil {
		return nil, err
	}
	return parsePS(printed), nil
}

// psColumns is how many tab-separated fields psFormat produces.
const psColumns = 6

// parsePS reads `docker ps --format psFormat` output. A row with fewer columns
// than the template asks for is skipped: it is not a container this package can
// describe.
func parsePS(printed []byte) []container {
	var listed []container
	rows := bufio.NewScanner(strings.NewReader(string(printed)))
	for rows.Scan() {
		fields := strings.SplitN(rows.Text(), "\t", psColumns)
		if len(fields) < psColumns {
			continue
		}
		listed = append(listed, container{
			name:              fields[0],
			image:             fields[1],
			portMappings:      parsePorts(fields[2]),
			composeService:    fields[3],
			composeProject:    fields[4],
			composeWorkingDir: fields[5],
		})
	}
	return listed
}

// parsePorts reads Docker's port list, where each entry reads
// "[host]:<hostPort>-><containerPort>/<proto>" and entries are comma-separated.
// Entries this package cannot use — no arrow, no host port — are dropped rather
// than guessed at.
func parsePorts(printed string) []portMapping {
	if printed == "" {
		return nil
	}
	var published []portMapping
	for _, entry := range strings.Split(printed, ", ") {
		mapping, usable := parsePortEntry(entry)
		if usable {
			published = append(published, mapping)
		}
	}
	return published
}

// parsePortEntry reads one published-port entry. The host side is everything
// before the arrow and the container side everything after it; the container
// side may carry a "/tcp" or "/udp" suffix, which this package does not use.
func parsePortEntry(entry string) (portMapping, bool) {
	hostSide, containerSide, found := strings.Cut(entry, "->")
	if !found {
		return portMapping{}, false
	}
	// The address part can be a bare IPv6 address, so the port is whatever
	// follows the last colon.
	_, hostPortText, found := strings.Cut(hostSide, ":")
	if !found {
		return portMapping{}, false
	}
	hostPort, err := strconv.Atoi(hostPortText)
	if err != nil {
		return portMapping{}, false
	}
	containerPortText, _, _ := strings.Cut(containerSide, "/")
	containerPort, err := strconv.Atoi(containerPortText)
	if err != nil {
		return portMapping{}, false
	}
	return portMapping{hostPort: hostPort, containerPort: containerPort}, true
}
