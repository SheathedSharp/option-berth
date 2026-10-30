package groupstart

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

// errNoPrepareInputs means the service does not declare inputs, so there is no
// stable fact to compare and the step must run every time.
var errNoPrepareInputs = errors.New("no prepare_inputs declared")

// prepareStampPath is where one service's last successful fingerprint lives.
// It is keyed by the manifest's canonical path plus the service name: two
// worktrees of one repository are two checkouts with two prepare states, and
// renaming a service must not inherit the old name's stamp.
func prepareStampPath(configPath, service string) string {
	sum := sha256.Sum256([]byte(configPath + "\x00" + service))
	return filepath.Join(paths.Prepare(), hex.EncodeToString(sum[:])+".stamp")
}

// prepareDigest fingerprints the prepare command and the contents of every
// declared input. Inputs are read relative to the service's cwd. A missing or
// unreadable input is not "unchanged": it makes the step run, because a cache
// must never be the reason a build is skipped.
func prepareDigest(svc groups.Service, cwd string) (string, error) {
	if len(svc.PrepareInputs) == 0 {
		return "", errNoPrepareInputs
	}
	inputs := append([]string{}, svc.PrepareInputs...)
	sort.Strings(inputs)
	h := sha256.New()
	writeField(h, "command")
	writeField(h, svc.Prepare)
	seen := map[string]bool{}
	for _, input := range inputs {
		if seen[input] {
			continue
		}
		seen[input] = true
		path := filepath.Join(cwd, filepath.FromSlash(input))
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("prepare input %s: %w", input, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("prepare input %s is a directory", input)
		}
		sum, err := fileDigest(path)
		if err != nil {
			return "", fmt.Errorf("prepare input %s: %w", input, err)
		}
		writeField(h, input)
		writeField(h, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeField length-prefixes one field so ("ab", "c") and ("a", "bc") hash
// differently.
func writeField(w io.Writer, s string) {
	fmt.Fprintf(w, "%d:", len(s))
	io.WriteString(w, s)
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// prepareUnchanged reports whether the last successful prepare for this
// service saw exactly these inputs. Any error is "not unchanged": the caller
// runs the command, which is the safe direction.
func prepareUnchanged(configPath string, svc groups.Service, cwd string) bool {
	digest, err := prepareDigest(svc, cwd)
	if err != nil {
		return false
	}
	stamp, err := os.ReadFile(prepareStampPath(configPath, svc.Name))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(stamp)) == digest
}

// recordPrepareStamp writes the fingerprint of the step that just succeeded.
// It does nothing when no inputs are declared, or when a declared input is
// missing now; the next start then reruns rather than trusting a partial key.
func recordPrepareStamp(configPath string, svc groups.Service, cwd string) error {
	digest, err := prepareDigest(svc, cwd)
	if err != nil {
		if errors.Is(err, errNoPrepareInputs) {
			return nil
		}
		return err
	}
	dir := paths.Prepare()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".prepare-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := io.WriteString(tmp, digest+"\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, prepareStampPath(configPath, svc.Name))
}
