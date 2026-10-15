package agentstream

import (
	"fmt"
	"os/exec"
)

type liveDriver struct {
	provider   string
	worktree   string
	resumeFile string
	lookPath   func(string) (string, error)
	inner      driver
}

func newLiveDriver(provider, worktree, resumeFile string, lookPath func(string) (string, error)) driver {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	switch provider {
	case "codex":
		return &liveDriver{
			provider: provider, worktree: worktree, resumeFile: resumeFile, lookPath: lookPath,
			inner: newCodexLive(worktree, resumeFile, lookPath),
		}
	case "claude":
		return &liveDriver{
			provider: provider, worktree: worktree, resumeFile: resumeFile, lookPath: lookPath,
			inner: newClaudeLive(worktree, resumeFile, lookPath),
		}
	case "opencode":
		return &liveDriver{
			provider: provider, worktree: worktree, resumeFile: resumeFile, lookPath: lookPath,
			inner: newOpenCodeLive(worktree, resumeFile, lookPath),
		}
	case "pi":
		return &liveDriver{
			provider: provider, worktree: worktree, resumeFile: resumeFile, lookPath: lookPath,
			inner: newPiLive(worktree, resumeFile, lookPath),
		}
	default:
		return &liveDriver{provider: provider, worktree: worktree, resumeFile: resumeFile, lookPath: lookPath}
	}
}

func (l *liveDriver) turnStart(b *bridge, id, text string) error {
	if l.inner != nil {
		return l.inner.turnStart(b, id, text)
	}
	b.emitError(id, "unsupported", fmt.Sprintf("live %s stream adapter is not implemented in this build; set BERTH_AGENT_STREAM_FAKE=%s for tests", l.provider, l.provider))
	return nil
}

func (l *liveDriver) turnInterrupt(b *bridge, id string) error {
	if l.inner != nil {
		return l.inner.turnInterrupt(b, id)
	}
	b.emitError(id, "unsupported", "live stream adapter is not implemented")
	return nil
}

func (l *liveDriver) requestRespond(b *bridge, id, request, decision string) error {
	if l.inner != nil {
		return l.inner.requestRespond(b, id, request, decision)
	}
	b.emitError(id, "unsupported", "live stream adapter is not implemented")
	return nil
}

func (l *liveDriver) sessionEnd(b *bridge) {
	if l.inner != nil {
		l.inner.sessionEnd(b)
	}
}
