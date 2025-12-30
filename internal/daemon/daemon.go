package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/taigrr/crush-lsp/internal/protocol"
	"github.com/taigrr/crush-lsp/internal/session"
	"github.com/taigrr/crush-lsp/internal/state"
	"github.com/taigrr/crush-lsp/internal/transport"
)

// Daemon manages the crush-lsp daemon process.
type Daemon struct {
	sessionManager *session.Manager
	logger         *log.Logger

	handlers map[string]*protocol.Handler
	mu       sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
}

// NewDaemon creates a new daemon.
func NewDaemon(logger *log.Logger) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())

	return &Daemon{
		sessionManager: session.NewManager(),
		logger:         logger,
		handlers:       make(map[string]*protocol.Handler),
		ctx:            ctx,
		cancel:         cancel,
	}
}

// Run starts the daemon and blocks until shutdown.
func (d *Daemon) Run() error {
	d.logger.Println("Daemon starting...")

	// Cleanup stale sessions from previous runs
	if err := d.sessionManager.CleanupStaleSessions(); err != nil {
		d.logger.Printf("Warning: failed to cleanup stale sessions: %v", err)
	}

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		d.logger.Println("Received shutdown signal")
		d.cancel()
	}()

	<-d.ctx.Done()
	d.logger.Println("Daemon shutting down...")

	return nil
}

// CreateSession creates a new session and returns its ID.
func (d *Daemon) CreateSession(workspaceRoot string, neovimPID int) (*session.Session, error) {
	sess, err := d.sessionManager.CreateSession(workspaceRoot, neovimPID)
	if err != nil {
		return nil, err
	}

	handler := protocol.NewHandler(sess.State(), d.logger)

	d.mu.Lock()
	d.handlers[sess.ID] = handler
	d.mu.Unlock()

	d.logger.Printf("Created session %s for workspace %s", sess.ID, workspaceRoot)
	return sess, nil
}

// DiscoverSession finds or creates a session for a workspace.
func (d *Daemon) DiscoverSession(workspaceRoot string, neovimPID int) (*session.Session, error) {
	sess, err := d.sessionManager.DiscoverSession(workspaceRoot, neovimPID)
	if err != nil {
		return nil, err
	}

	// Ensure handler exists
	d.mu.Lock()
	if _, ok := d.handlers[sess.ID]; !ok {
		d.handlers[sess.ID] = protocol.NewHandler(sess.State(), d.logger)
	}
	d.mu.Unlock()

	d.logger.Printf("Using session %s for workspace %s", sess.ID, workspaceRoot)
	return sess, nil
}

// GetSession retrieves a session by ID.
func (d *Daemon) GetSession(id string) (*session.Session, error) {
	return d.sessionManager.GetOrLoadSession(id)
}

// GetHandler retrieves the handler for a session.
func (d *Daemon) GetHandler(sessionID string) (*protocol.Handler, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	h, ok := d.handlers[sessionID]
	return h, ok
}

// ServeNeovim handles a Neovim LSP connection for a session.
func (d *Daemon) ServeNeovim(sessionID string, t transport.Transport) error {
	sess, err := d.sessionManager.GetOrLoadSession(sessionID)
	if err != nil {
		return err
	}

	handler, ok := d.GetHandler(sessionID)
	if !ok {
		// Create handler if it doesn't exist
		handler = protocol.NewHandler(sess.State(), d.logger)
		d.mu.Lock()
		d.handlers[sessionID] = handler
		d.mu.Unlock()
	}

	client := &protocol.Client{
		ID:        fmt.Sprintf("neovim-%s", sessionID),
		Type:      protocol.ClientTypeNeovim,
		Transport: t,
	}

	handler.AddClient(client)
	defer handler.RemoveClient(client.ID)

	d.logger.Printf("Neovim connected to session %s", sessionID)

	return d.serveClient(handler, client)
}

// ServeCrush handles a Crush socket connection for a session.
func (d *Daemon) ServeCrush(sessionID string, t transport.Transport) error {
	sess, err := d.sessionManager.GetOrLoadSession(sessionID)
	if err != nil {
		return err
	}

	handler, ok := d.GetHandler(sessionID)
	if !ok {
		handler = protocol.NewHandler(sess.State(), d.logger)
		d.mu.Lock()
		d.handlers[sessionID] = handler
		d.mu.Unlock()
	}

	client := &protocol.Client{
		ID:        fmt.Sprintf("crush-%s", sessionID),
		Type:      protocol.ClientTypeCrush,
		Transport: t,
	}

	handler.AddClient(client)
	defer handler.RemoveClient(client.ID)

	d.logger.Printf("Crush connected to session %s", sessionID)

	return d.serveClient(handler, client)
}

// serveClient reads messages from a client and dispatches to handler.
func (d *Daemon) serveClient(handler *protocol.Handler, client *protocol.Client) error {
	for {
		select {
		case <-d.ctx.Done():
			return d.ctx.Err()
		default:
		}

		method, content, err := client.Transport.Read()
		if err != nil {
			d.logger.Printf("Client %s read error: %v", client.ID, err)
			return err
		}

		if err := handler.HandleMessage(client, method, content); err != nil {
			d.logger.Printf("Handler error for %s: %v", client.ID, err)
		}
	}
}

// RemoveSession removes a session and cleans up resources.
func (d *Daemon) RemoveSession(sessionID string) error {
	d.mu.Lock()
	delete(d.handlers, sessionID)
	d.mu.Unlock()

	return d.sessionManager.RemoveSession(sessionID)
}

// Shutdown gracefully shuts down the daemon.
func (d *Daemon) Shutdown() {
	d.cancel()
}

// RunStandalone runs the daemon in standalone mode for a single session.
// This is useful for direct LSP mode without daemon infrastructure.
func RunStandalone(logger *log.Logger) error {
	st := state.NewState()
	handler := protocol.NewHandler(st, logger)

	t := transport.NewStdioTransport(os.Stdin, os.Stdout)

	client := &protocol.Client{
		ID:        "neovim-standalone",
		Type:      protocol.ClientTypeNeovim,
		Transport: t,
	}

	handler.AddClient(client)

	logger.Println("Running in standalone LSP mode")

	for {
		method, content, err := t.Read()
		if err != nil {
			return err
		}

		if err := handler.HandleMessage(client, method, content); err != nil {
			logger.Printf("Handler error: %v", err)
		}
	}
}
