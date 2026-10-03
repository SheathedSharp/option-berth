package ports

import (
	"context"
	"errors"
	"testing"
)

func TestScanContextPreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err := ScanContext(ctx)
	if !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatalf("cancelled scan=%v, %v", rows, err)
	}
}
