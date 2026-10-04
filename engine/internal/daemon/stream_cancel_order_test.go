package daemon

import (
	"bufio"
	"encoding/json"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"net"
	"testing"
)

func TestCancellationAcknowledgmentAndEndEitherOrder(t *testing.T) {
	for _, endFirst := range []bool{true, false} {
		name := "ack first"
		if endFirst {
			name = "end first"
		}
		t.Run(name, func(t *testing.T) {
			server, client := net.Pipe()
			t.Cleanup(func() { client.Close(); server.Close() })
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				var request rpc.Request
				if err := json.NewDecoder(server).Decode(&request); err != nil {
					done <- err
					return
				}
				end := map[string]any{"jsonrpc": rpc.Version, "method": rpc.MethodStreamEnd, "params": rpc.StreamEnd{ID: "fixture"}}
				reply := map[string]any{"jsonrpc": rpc.Version, "id": request.ID, "result": rpc.OKResult{OK: true}}
				frames := []any{reply, end}
				if endFirst {
					frames = []any{end, reply}
				}
				for _, frame := range frames {
					if err := json.NewEncoder(server).Encode(frame); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			c := &testClient{t: t, conn: client, r: bufio.NewReader(client)}
			end := c.cancelStreamAndAwaitEnd("fixture")
			if end.ID != "fixture" || end.Error != nil {
				t.Fatal(end)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
