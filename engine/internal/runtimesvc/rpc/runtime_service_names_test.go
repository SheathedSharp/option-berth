package rpc

import (
	"strings"
	"testing"
)

func TestRetiredHostMethodsAreRefusedWithoutAliases(t *testing.T) {
	for _, method := range []string{"daemon.hello", "daemon.shutdown", "daemon.status", "daemon.schema", "daemon.doctor"} {
		err := RetiredRuntimeServiceMethodError(method)
		if err == nil || err.Code != CodeNotFound || !strings.Contains(err.Message, "protocol 3") || !strings.Contains(err.Data.Hint, "runtime.service") {
			t.Fatalf("missing refusal for %s: %+v", method, err)
		}
	}
	for _, method := range []string{"runtime.service.hello", "groups.status", "state.subscribe"} {
		if err := RetiredRuntimeServiceMethodError(method); err != nil {
			t.Fatalf("rejected active method: %+v", err)
		}
	}
}
