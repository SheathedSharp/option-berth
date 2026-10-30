package scanner

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestConfiguredTCPHealthJoinsThePort(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	health := fmt.Sprintf("tcp:%d", port)
	gg := []state.Group{{Services: []state.Service{{
		Name: "db", Running: true, Port: &port, PortActual: &port, Health: &health,
	}}}}
	rows := []state.Port{{Port: port, BindAddress: "127.0.0.1"}}

	if err := ProbeConfiguredContext(context.Background(), rows, gg, nil, 0); err != nil {
		t.Fatalf("ProbeConfiguredContext: %v", err)
	}
	if rows[0].Health == nil || rows[0].Health.Status != state.HealthOK || !rows[0].Health.Configured {
		t.Fatalf("port health = %+v, want configured ok", rows[0].Health)
	}
	if got := gg[0].Services[0].HealthStatus; got == nil || got.Status != state.HealthOK {
		t.Fatalf("service health = %+v, want ok", got)
	}
}

func TestConfiguredCommandHealthNeedsNoListener(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	health := "command:sh -c 'exit 0'"
	gg := []state.Group{{RootDir: &dir, Services: []state.Service{{
		Name: "worker", Running: true, Health: &health,
	}}}}

	if err := ProbeConfiguredContext(context.Background(), nil, gg, nil, 0); err != nil {
		t.Fatalf("ProbeConfiguredContext: %v", err)
	}
	got := gg[0].Services[0].HealthStatus
	if got == nil || got.Status != state.HealthOK || !got.Configured {
		t.Fatalf("service health = %+v, want configured ok", got)
	}
}

func TestConfiguredCommandHealthExpandsTheServicePort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	port := 54321
	health := "command:sh -c 'test ${port} -eq 54321'"
	gg := []state.Group{{RootDir: &dir, Services: []state.Service{{
		Name: "db", Running: true, Port: &port, PortActual: &port, Health: &health,
	}}}}
	if err := ProbeConfiguredContext(context.Background(), nil, gg, nil, 0); err != nil {
		t.Fatalf("ProbeConfiguredContext: %v", err)
	}
	got := gg[0].Services[0].HealthStatus
	if got == nil || got.Status != state.HealthOK {
		t.Fatalf("service health = %+v, want ok", got)
	}
}

func TestCommandHealthIsClearedWhenTheServiceStops(t *testing.T) {
	health := "command:true"
	previous := &state.Health{Status: state.HealthOK, Configured: true}
	gg := []state.Group{{Services: []state.Service{{
		Name: "worker", Running: false, Health: &health, HealthStatus: previous,
	}}}}
	if err := ProbeConfiguredContext(context.Background(), nil, gg, nil, 0); err != nil {
		t.Fatalf("ProbeConfiguredContext: %v", err)
	}
	if gg[0].Services[0].HealthStatus != nil {
		t.Fatalf("health_status = %+v, want nil for a stopped command-checked service", gg[0].Services[0].HealthStatus)
	}
}
