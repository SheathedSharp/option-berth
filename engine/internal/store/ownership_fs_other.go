//go:build !darwin && !linux && !windows

package store

import "fmt"

func requireLocalFilesystem(string) error {
	return fmt.Errorf("Runtime Service storage filesystem ownership is not verified on this platform")
}
