package transport

import (
	"bufio"
	"io"
	"sync"

	"github.com/taigrr/crush-lsp/rpc"
)

// Transport represents a bidirectional LSP transport.
type Transport interface {
	// Read reads a single LSP message (method and content).
	Read() (method string, content []byte, err error)
	// Write writes an LSP message.
	Write(msg any) error
	// Close closes the transport.
	Close() error
}

// StdioTransport implements Transport over stdin/stdout.
type StdioTransport struct {
	reader  *bufio.Scanner
	writer  io.Writer
	writeMu sync.Mutex
	closed  bool
	closeMu sync.Mutex
}

// NewStdioTransport creates a new stdio transport.
func NewStdioTransport(reader io.Reader, writer io.Writer) *StdioTransport {
	scanner := bufio.NewScanner(reader)
	scanner.Split(rpc.Split)

	return &StdioTransport{
		reader: scanner,
		writer: writer,
	}
}

// Read reads a single LSP message.
func (t *StdioTransport) Read() (string, []byte, error) {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return "", nil, io.EOF
	}
	t.closeMu.Unlock()

	if !t.reader.Scan() {
		if err := t.reader.Err(); err != nil {
			return "", nil, err
		}
		return "", nil, io.EOF
	}

	msg := t.reader.Bytes()
	return rpc.DecodeMessage(msg)
}

// Write writes an LSP message.
func (t *StdioTransport) Write(msg any) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return io.ErrClosedPipe
	}
	t.closeMu.Unlock()

	reply := rpc.EncodeMessage(msg)
	_, err := t.writer.Write([]byte(reply))
	return err
}

// Close closes the transport.
func (t *StdioTransport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	t.closed = true
	return nil
}
