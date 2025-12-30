// Package session manages crush-lsp session files for daemon coordination
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/taigrr/crush-lsp/internal/state"
)

const (
	// SessionFileName is the name of the session file in workspace .crush folder.
	SessionFileName = "session"
	// SocketDirName is the name of the socket directory in runtime dir.
	SocketDirName = "crush-lsp"
)

// Session represents a paired Neovim/Crush session.
// It manages the connection state between a Neovim instance and
// a Crush AI agent, enabling bidirectional communication and state sync.
type Session struct {
	ID            string    `json:"id"`
	WorkspaceRoot string    `json:"workspace_root"`
	NeovimPID     int       `json:"neovim_pid,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	SocketPath    string    `json:"socket_path"`

	state *state.State
	mu    sync.RWMutex
}

// SessionMetadata is the JSON-serializable session info stored in workspace.
type SessionMetadata struct {
	ID            string    `json:"id"`
	WorkspaceRoot string    `json:"workspace_root"`
	NeovimPID     int       `json:"neovim_pid,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	SocketPath    string    `json:"socket_path"`
}

// Manager handles multiple concurrent sessions.
type Manager struct {
	mu        sync.RWMutex
	sessions  map[string]*Session
	socketDir string
}

// NewManager creates a new session manager.
func NewManager() *Manager {
	return &Manager{
		sessions:  make(map[string]*Session),
		socketDir: getSecureSocketDir(),
	}
}

// getSecureSocketDir returns a secure directory for sockets.
// Uses XDG_RUNTIME_DIR on Linux, falls back to TMPDIR with UID on macOS.
func getSecureSocketDir() string {
	// Try XDG_RUNTIME_DIR first (Linux standard, secure tmpfs)
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, SocketDirName)
	}

	// macOS/fallback: use TMPDIR with UID for isolation
	tmpDir := os.TempDir()
	uid := os.Getuid()
	return filepath.Join(tmpDir, fmt.Sprintf("%s-%d", SocketDirName, uid))
}

// ensureSecureSocketDir creates the socket directory with secure permissions.
func (m *Manager) ensureSecureSocketDir() error {
	// Create with 0700 - only owner can access
	if err := os.MkdirAll(m.socketDir, 0700); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Verify permissions (in case dir already existed with wrong perms)
	info, err := os.Stat(m.socketDir)
	if err != nil {
		return err
	}

	// On Unix, ensure it's owner-only
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0700 {
			if err := os.Chmod(m.socketDir, 0700); err != nil {
				return fmt.Errorf("failed to set socket directory permissions: %w", err)
			}
		}
	}

	return nil
}

// GenerateSessionID creates a new unique session ID.
func GenerateSessionID() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate session ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// CreateSession creates a new session with a unique ID.
// The session file is written to <workspaceRoot>/.crush/session
// The socket is created in the secure runtime directory.
func (m *Manager) CreateSession(workspaceRoot string, neovimPID int) (*Session, error) {
	id, err := GenerateSessionID()
	if err != nil {
		return nil, err
	}

	// Ensure secure socket directory exists
	if err := m.ensureSecureSocketDir(); err != nil {
		return nil, err
	}

	// Socket goes in secure runtime directory
	socketPath := filepath.Join(m.socketDir, id+".sock")

	session := &Session{
		ID:            id,
		WorkspaceRoot: workspaceRoot,
		NeovimPID:     neovimPID,
		CreatedAt:     time.Now(),
		SocketPath:    socketPath,
		state:         state.NewState(),
	}

	// Save session file to workspace .crush folder
	if err := m.saveWorkspaceSessionFile(session); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()

	return session, nil
}

// GetSession retrieves a session by ID.
func (m *Manager) GetSession(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[id]
	return session, ok
}

// GetOrLoadSession retrieves a session, loading from workspace if necessary.
func (m *Manager) GetOrLoadSession(id string) (*Session, error) {
	// Check in-memory first
	m.mu.RLock()
	if session, ok := m.sessions[id]; ok {
		m.mu.RUnlock()
		return session, nil
	}
	m.mu.RUnlock()

	// Session not in memory - caller needs to provide workspace to load from
	return nil, fmt.Errorf("session %s not found in memory", id)
}

// LoadSessionFromWorkspace loads a session from a workspace's .crush/session file.
// If checkSocket is true, verifies the socket exists and removes stale sessions.
func (m *Manager) LoadSessionFromWorkspace(workspaceRoot string) (*Session, error) {
	return m.loadSessionFromWorkspace(workspaceRoot, true)
}

// LoadSessionMetadata loads a session without checking if socket exists.
// Used by daemon which needs to read session info before creating the socket.
func (m *Manager) LoadSessionMetadata(workspaceRoot string) (*Session, error) {
	return m.loadSessionFromWorkspace(workspaceRoot, false)
}

func (m *Manager) loadSessionFromWorkspace(workspaceRoot string, checkSocket bool) (*Session, error) {
	sessionFile := filepath.Join(workspaceRoot, ".crush", SessionFileName)

	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("no session file found: %w", err)
	}

	var meta SessionMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	// Verify socket still exists (only if requested)
	if checkSocket {
		if _, err := os.Stat(meta.SocketPath); err != nil {
			// Socket gone, session is stale
			os.Remove(sessionFile)
			return nil, fmt.Errorf("session socket no longer exists")
		}
	}

	session := &Session{
		ID:            meta.ID,
		WorkspaceRoot: meta.WorkspaceRoot,
		NeovimPID:     meta.NeovimPID,
		CreatedAt:     meta.CreatedAt,
		SocketPath:    meta.SocketPath,
		state:         state.NewState(),
	}

	m.mu.Lock()
	m.sessions[meta.ID] = session
	m.mu.Unlock()

	return session, nil
}

// DiscoverSession finds or creates a session for a workspace.
// If a valid session file exists, loads it. Otherwise creates a new one.
func (m *Manager) DiscoverSession(workspaceRoot string, neovimPID int) (*Session, error) {
	// Try to load existing session
	session, err := m.LoadSessionFromWorkspace(workspaceRoot)
	if err == nil {
		return session, nil
	}

	// No valid session, create new one
	return m.CreateSession(workspaceRoot, neovimPID)
}

// ListSessions returns all active session IDs.
func (m *Manager) ListSessions() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// FindSessionByWorkspace finds a session by workspace root.
func (m *Manager) FindSessionByWorkspace(workspaceRoot string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, session := range m.sessions {
		if session.WorkspaceRoot == workspaceRoot {
			return session, true
		}
	}
	return nil, false
}

// RemoveSession removes a session and cleans up resources.
func (m *Manager) RemoveSession(id string) error {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	if !ok {
		return nil
	}

	// Clean up socket
	os.Remove(session.SocketPath)

	// Clean up workspace session file
	sessionFile := filepath.Join(session.WorkspaceRoot, ".crush", SessionFileName)
	os.Remove(sessionFile)

	return nil
}

// State returns the session's shared state.
func (s *Session) State() *state.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// saveWorkspaceSessionFile writes session info to workspace .crush/session file.
func (m *Manager) saveWorkspaceSessionFile(session *Session) error {
	crushDir := filepath.Join(session.WorkspaceRoot, ".crush")

	// Create .crush directory if it doesn't exist
	if err := os.MkdirAll(crushDir, 0755); err != nil {
		return fmt.Errorf("failed to create .crush directory: %w", err)
	}

	meta := SessionMetadata{
		ID:            session.ID,
		WorkspaceRoot: session.WorkspaceRoot,
		NeovimPID:     session.NeovimPID,
		CreatedAt:     session.CreatedAt,
		SocketPath:    session.SocketPath,
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session metadata: %w", err)
	}

	sessionFile := filepath.Join(crushDir, SessionFileName)
	if err := os.WriteFile(sessionFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	return nil
}

// CleanupStaleSessions removes sessions whose Neovim process is no longer running.
func (m *Manager) CleanupStaleSessions() error {
	// Clean up sockets in runtime dir that don't have a live process
	entries, err := os.ReadDir(m.socketDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sock" {
			continue
		}

		socketPath := filepath.Join(m.socketDir, entry.Name())

		// Try to determine if socket is stale
		// A socket is stale if we can't connect to it
		// For now, just remove sockets older than 24 hours
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if time.Since(info.ModTime()) > 24*time.Hour {
			os.Remove(socketPath)
		}
	}

	return nil
}

// CleanupOnShutdown removes the session's socket and session file.
func (m *Manager) CleanupOnShutdown(sessionID string) {
	m.RemoveSession(sessionID)
}

// GetSocketPath returns the socket path for a session ID.
func (m *Manager) GetSocketPath(sessionID string) string {
	return filepath.Join(m.socketDir, sessionID+".sock")
}

// IsProcessAlive checks if a process with the given PID is still running.
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix, FindProcess always succeeds. Send signal 0 to check if alive.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}
