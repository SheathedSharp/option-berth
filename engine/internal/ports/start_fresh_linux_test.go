//go:build linux

package ports

import (
	"strings"
	"testing"
	"time"
)

func TestProcStartTimeParsingAndClockFrequency(t *testing.T) {
	row := "42 (worker ) with space) S " + strings.Repeat("0 ", 18) + "12345 0"
	got, ok := procStartTime(row, "cpu 1 2 3\nbtime 1700000000\n", 42, 250)
	if !ok || !got.Equal(time.Unix(1700000049, 380000000)) {
		t.Fatalf("got %v %v", got, ok)
	}
	for _, item := range []struct {
		stat, boot string
		pid        int
		hz         uint64
	}{
		{row, "btime 1700000000", 41, 250},
		{row, "btime 1700000000", 42, 0},
		{row, "btime 1700000000", 42, 1000000001},
		{"42 (x) S 0", "btime 1700000000", 42, 250},
		{row, "btime invalid", 42, 250},
		{row, "btime -1", 42, 250},
		{row, "btime 9223372036854775807", 42, 250},
		{strings.Replace(row, "12345", "-1", 1), "btime 1700000000", 42, 250},
		{row, "cpu 0 0 0", 42, 250},
	} {
		if at, ok := procStartTime(item.stat, item.boot, item.pid, item.hz); ok {
			t.Fatalf("invalid input returned %v", at)
		}
	}
}
