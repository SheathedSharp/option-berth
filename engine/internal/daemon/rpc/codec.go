package rpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrOversize marks a message that exceeded the decoder's per-message limit.
// The offending line is drained rather than left in the buffer, so the decoder
// resynchronises on the next message instead of reading fragments of this one.
var ErrOversize = errors.New("rpc: message too large")

// defaultMaxMessage bounds one message when the caller gives no limit.
const defaultMaxMessage = 4 << 20

// readChunk is how much ReadSlice hands back per call. It is a buffer size, not
// a limit: the message limit is enforced by accumulating across chunks.
const readChunk = 64 << 10

// Encoder writes newline-delimited JSON.
//
// The lock is not incidental. The daemon answers calls and pushes broadcast
// notifications from different goroutines onto one connection, and two
// concurrent Encodes would interleave their bytes into a line neither side can
// parse.
type Encoder struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{encoder: json.NewEncoder(w)}
}

// Encode writes one message followed by a newline.
func (e *Encoder) Encode(message any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.encoder.Encode(message)
}

// Decoder reads newline-delimited JSON with a per-message size limit.
type Decoder struct {
	reader *bufio.Reader
	max    int
}

// NewDecoder returns a Decoder reading from r that rejects any single message
// longer than max bytes. A max of zero or less means defaultMaxMessage.
func NewDecoder(r io.Reader, max int) *Decoder {
	if max <= 0 {
		max = defaultMaxMessage
	}
	return &Decoder{reader: bufio.NewReaderSize(r, readChunk), max: max}
}

// Next returns the next message.
//
//	io.EOF            the stream ended cleanly
//	ErrOversize       the message was longer than the limit
//	*Error            the line was not valid JSON (CodeInvalidParams)
func (d *Decoder) Next() (Message, error) {
	line, err := d.nextNonBlankLine()
	if err != nil {
		return Message{}, err
	}
	var message Message
	if err := json.Unmarshal(line, &message); err != nil {
		return Message{}, NewError(CodeInvalidParams,
			fmt.Sprintf("malformed JSON message: %v", err),
			"messages must be single-line JSON-RPC 2.0 objects")
	}
	return message, nil
}

// nextNonBlankLine skips keepalive lines. A blank line is not a message, and
// treating one as a protocol error would close an idle connection that is
// working exactly as intended.
func (d *Decoder) nextNonBlankLine() ([]byte, error) {
	for {
		line, err := d.readLine()
		if err != nil {
			return nil, err
		}
		if len(line) > 0 {
			return line, nil
		}
	}
}

// readLine returns one line without its terminator, enforcing the size limit.
//
// An oversize line is read to its end and dropped rather than returned: the
// bytes are already refused, and stopping mid-line would leave the rest of that
// message to be parsed as the next one.
func (d *Decoder) readLine() ([]byte, error) {
	var (
		line    []byte
		total   int
		tooLong bool
	)
	for {
		chunk, err := d.reader.ReadSlice('\n')
		total += len(chunk)
		if total > d.max {
			tooLong = true
			line = nil // release the payload we are no longer going to return
		} else if !tooLong {
			line = append(line, chunk...)
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue // the line continues; keep accumulating
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		// Either the line ended, or the stream did without a final newline.
		if tooLong {
			return nil, oversizeError(total, d.max)
		}
		if errors.Is(err, io.EOF) && total == 0 {
			return nil, io.EOF
		}
		return trimEOL(line), nil
	}
}

// oversizeError names the size and the limit, so the reader can tell a
// misconfigured client from a protocol violation.
func oversizeError(total, max int) error {
	return fmt.Errorf("%w: %d bytes exceeds limit %d", ErrOversize, total, max)
}

// trimEOL strips a trailing \n and an optional \r before it, so a Windows line
// ending does not end up inside the parsed JSON.
func trimEOL(line []byte) []byte {
	line = bytesTrimSuffix(line, '\n')
	return bytesTrimSuffix(line, '\r')
}

// bytesTrimSuffix drops the last byte when it is want.
func bytesTrimSuffix(line []byte, want byte) []byte {
	if len(line) > 0 && line[len(line)-1] == want {
		return line[:len(line)-1]
	}
	return line
}
