package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// helloCall is the one message the round-trip case puts through the encoder.
func helloCall() Request {
	return Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "daemon.hello"}
}

// A message written by the encoder must come back from the decoder as the same
// message; a message longer than the decoder's limit is refused as a whole
// rather than truncated into whatever follows it.
func TestCodecRoundTripAndOversize(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		var wire bytes.Buffer
		if err := NewEncoder(&wire).Encode(helloCall()); err != nil {
			t.Fatalf("Encode: %v", err)
		}

		msg, err := NewDecoder(&wire, 4<<20).Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if msg.Method != "daemon.hello" || !msg.IsRequest() {
			t.Fatalf("decoded %+v, want the daemon.hello call that was encoded", msg)
		}
	})

	t.Run("over the limit", func(t *testing.T) {
		payload := strings.Repeat("y", 5<<20)
		wire := `{"jsonrpc":"2.0","method":"` + payload + `"}` + "\n"
		if _, err := NewDecoder(strings.NewReader(wire), 4<<20).Next(); !errors.Is(err, ErrOversize) {
			t.Fatalf("%d-byte message: err = %v, want ErrOversize", len(wire), err)
		}
	})
}

// A method without an id is a notification the client must not answer, and a
// message with an id and no method is a reply. Both arrive on the same stream.
func TestDecoderClassifiesNotifications(t *testing.T) {
	wire := `{"jsonrpc":"2.0","method":"stream.chunk","params":{"id":"s1"}}` + "\n" +
		`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}` + "\n"
	dec := NewDecoder(strings.NewReader(wire), 1<<20)

	first, err := dec.Next()
	if err != nil {
		t.Fatalf("first message: %v", err)
	}
	if !first.IsNotification() || first.Method != "stream.chunk" {
		t.Fatalf("first message = %+v, want a stream.chunk notification", first)
	}

	second, err := dec.Next()
	if err != nil {
		t.Fatalf("second message: %v", err)
	}
	if second.IsNotification() {
		t.Fatalf("a message carrying an id was classified as a notification: %+v", second)
	}
	if !second.IsResponse() {
		t.Fatalf("second message = %+v, want a reply", second)
	}
	if string(second.ID) != "7" {
		t.Fatalf("id = %s, want 7", second.ID)
	}
}

// A stream that ends before any message is a clean end, not an error to report.
func TestDecoderEOF(t *testing.T) {
	if _, err := NewDecoder(strings.NewReader(""), 1<<20).Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream: err = %v, want io.EOF", err)
	}
}

// A keepalive blank line is skipped rather than parsed, and an oversize message
// is dropped whole so the message after it is still readable.
func TestDecoderSkipsBlankLinesAndRecoversAfterOversize(t *testing.T) {
	oversize := `{"jsonrpc":"2.0","method":"` + strings.Repeat("z", 300) + `"}`
	next := `{"jsonrpc":"2.0","id":1,"method":"daemon.status"}`
	dec := NewDecoder(strings.NewReader("\n"+oversize+"\n"+next+"\n"), 128)

	if _, err := dec.Next(); !errors.Is(err, ErrOversize) {
		t.Fatalf("err = %v, want ErrOversize", err)
	}
	msg, err := dec.Next()
	if err != nil {
		t.Fatalf("the decoder did not resynchronise after an oversize message: %v", err)
	}
	if msg.Method != "daemon.status" {
		t.Fatalf("message after the oversize one = %+v, want daemon.status", msg)
	}
}

// A line that is not JSON at all is the client's mistake, reported with the
// JSON-RPC code that says so.
func TestDecoderMalformedJSONIsInvalidParams(t *testing.T) {
	_, err := NewDecoder(strings.NewReader("{not json}\n"), 1<<20).Next()

	var invalid *Error
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want a *Error", err)
	}
	if invalid.Code != CodeInvalidParams {
		t.Fatalf("code = %d, want %d (invalid_params)", invalid.Code, CodeInvalidParams)
	}
}
