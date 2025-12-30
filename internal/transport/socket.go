package transport

import (
	"bufio"
	"educationalsp/rpc"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

// SocketTransport implements Transport over Unix socket.
type SocketTransport struct {
	conn    net.Conn
	reader  *bufio.Scanner
	writeMu sync.Mutex
	closed  bool
	closeMu sync.Mutex
}

// NewSocketTransport creates a transport from an existing connection.
func NewSocketTransport(conn net.Conn) *SocketTransport {
	scanner := bufio.NewScanner(conn)
	scanner.Split(rpc.Split)
	// Increase buffer size for large messages
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	return &SocketTransport{
		conn:   conn,
		reader: scanner,
	}
}

// Read reads a single LSP message.
func (t *SocketTransport) Read() (string, []byte, error) {
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
func (t *SocketTransport) Write(msg any) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return io.ErrClosedPipe
	}
	t.closeMu.Unlock()

	reply := rpc.EncodeMessage(msg)
	_, err := t.conn.Write([]byte(reply))
	return err
}

// Close closes the transport.
func (t *SocketTransport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true
	return t.conn.Close()
}

// SocketListener listens for socket connections.
type SocketListener struct {
	listener net.Listener
	path     string
}

// NewSocketListener creates a new Unix socket listener.
func NewSocketListener(path string) (*SocketListener, error) {
	// Remove existing socket file if present
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to remove existing socket: %w", err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on socket: %w", err)
	}

	return &SocketListener{
		listener: listener,
		path:     path,
	}, nil
}

// Accept accepts a new connection and returns a transport.
func (l *SocketListener) Accept() (*SocketTransport, error) {
	conn, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	return NewSocketTransport(conn), nil
}

// Close closes the listener and removes the socket file.
func (l *SocketListener) Close() error {
	err := l.listener.Close()
	os.Remove(l.path)
	return err
}

// Path returns the socket path.
func (l *SocketListener) Path() string {
	return l.path
}

// DialSocket connects to a Unix socket and returns a transport.
func DialSocket(path string) (*SocketTransport, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to socket: %w", err)
	}
	return NewSocketTransport(conn), nil
}
