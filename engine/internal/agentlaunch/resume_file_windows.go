//go:build windows

package agentlaunch

import (
	"errors"
	"os"
	"strings"
)

func openSessionFile(path string) (*os.File, error) {
	// Device/UNC names are not session documents. Only local regular files.
	if strings.HasPrefix(path, `\\`) {
		return nil, errors.New("device or UNC session path refused")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("session source is not regular")
	}
	return os.Open(path)
}
