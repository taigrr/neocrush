package session_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/taigrr/crush-lsp/internal/session"
)

func TestGenerateSessionID(t *testing.T) {
	id1, err := session.GenerateSessionID()
	if err != nil {
		t.Fatalf("Failed to generate session ID: %v", err)
	}

	if len(id1) != 16 { // 8 bytes = 16 hex chars
		t.Fatalf("Expected 16 char ID, got %d chars: %s", len(id1), id1)
	}

	// Generate another and ensure they're different
	id2, err := session.GenerateSessionID()
	if err != nil {
		t.Fatalf("Failed to generate second session ID: %v", err)
	}

	if id1 == id2 {
		t.Fatal("Generated IDs should be unique")
	}
}

func TestCreateSession(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	sess, err := mgr.CreateSession(tmpDir, 12345)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if sess.ID == "" {
		t.Fatal("Session ID should not be empty")
	}

	if sess.WorkspaceRoot != tmpDir {
		t.Fatalf("Expected workspace root %s, got %s", tmpDir, sess.WorkspaceRoot)
	}

	if sess.NeovimPID != 12345 {
		t.Fatalf("Expected PID 12345, got %d", sess.NeovimPID)
	}

	// Verify session file was created
	sessionFile := filepath.Join(tmpDir, ".crush", "session")
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("Session file not created: %v", err)
	}

	// Verify socket path is set
	if sess.SocketPath == "" {
		t.Fatal("Socket path should not be empty")
	}
}

func TestLoadSessionFromWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	// Create a session first
	created, err := mgr.CreateSession(tmpDir, 12345)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Create the socket file so LoadSessionFromWorkspace doesn't think it's stale
	socketDir := filepath.Dir(created.SocketPath)
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		t.Fatalf("Failed to create socket dir: %v", err)
	}
	f, err := os.Create(created.SocketPath)
	if err != nil {
		t.Fatalf("Failed to create socket file: %v", err)
	}
	f.Close()

	// Load it with a new manager
	mgr2 := session.NewManager()
	loaded, err := mgr2.LoadSessionFromWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load session: %v", err)
	}

	if loaded.ID != created.ID {
		t.Fatalf("Expected ID %s, got %s", created.ID, loaded.ID)
	}

	if loaded.SocketPath != created.SocketPath {
		t.Fatalf("Expected socket path %s, got %s", created.SocketPath, loaded.SocketPath)
	}
}

func TestLoadSessionMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	// Create a session first
	created, err := mgr.CreateSession(tmpDir, 12345)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// LoadSessionMetadata should work even without socket file
	mgr2 := session.NewManager()
	loaded, err := mgr2.LoadSessionMetadata(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load session metadata: %v", err)
	}

	if loaded.ID != created.ID {
		t.Fatalf("Expected ID %s, got %s", created.ID, loaded.ID)
	}
}

func TestLoadSessionFromWorkspace_StaleSession(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	// Create a session
	_, err := mgr.CreateSession(tmpDir, 12345)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Don't create the socket file - session should be considered stale
	mgr2 := session.NewManager()
	_, err = mgr2.LoadSessionFromWorkspace(tmpDir)
	if err == nil {
		t.Fatal("Expected error loading stale session, got nil")
	}

	// Session file should be removed
	sessionFile := filepath.Join(tmpDir, ".crush", "session")
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatal("Stale session file should have been removed")
	}
}

func TestRemoveSession(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	sess, err := mgr.CreateSession(tmpDir, 12345)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Create socket file
	socketDir := filepath.Dir(sess.SocketPath)
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		t.Fatalf("Failed to create socket dir: %v", err)
	}
	f, err := os.Create(sess.SocketPath)
	if err != nil {
		t.Fatalf("Failed to create socket file: %v", err)
	}
	f.Close()

	// Remove the session
	if err := mgr.RemoveSession(sess.ID); err != nil {
		t.Fatalf("Failed to remove session: %v", err)
	}

	// Verify socket was removed
	if _, err := os.Stat(sess.SocketPath); !os.IsNotExist(err) {
		t.Fatal("Socket file should have been removed")
	}

	// Verify session file was removed
	sessionFile := filepath.Join(tmpDir, ".crush", "session")
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatal("Session file should have been removed")
	}
}

func TestGetSession(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	sess, err := mgr.CreateSession(tmpDir, 12345)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Get the session
	retrieved, ok := mgr.GetSession(sess.ID)
	if !ok {
		t.Fatal("Session should exist")
	}

	if retrieved.ID != sess.ID {
		t.Fatalf("Expected ID %s, got %s", sess.ID, retrieved.ID)
	}

	// Try to get non-existent session
	_, ok = mgr.GetSession("nonexistent")
	if ok {
		t.Fatal("Non-existent session should not be found")
	}
}

func TestFindSessionByWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	sess, err := mgr.CreateSession(tmpDir, 12345)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Find by workspace
	found, ok := mgr.FindSessionByWorkspace(tmpDir)
	if !ok {
		t.Fatal("Session should be found by workspace")
	}

	if found.ID != sess.ID {
		t.Fatalf("Expected ID %s, got %s", sess.ID, found.ID)
	}

	// Try to find with non-existent workspace
	_, ok = mgr.FindSessionByWorkspace("/nonexistent")
	if ok {
		t.Fatal("Non-existent workspace should not be found")
	}
}

func TestListSessions(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()
	mgr := session.NewManager()

	sess1, err := mgr.CreateSession(tmpDir1, 12345)
	if err != nil {
		t.Fatalf("Failed to create session 1: %v", err)
	}

	sess2, err := mgr.CreateSession(tmpDir2, 12346)
	if err != nil {
		t.Fatalf("Failed to create session 2: %v", err)
	}

	ids := mgr.ListSessions()
	if len(ids) != 2 {
		t.Fatalf("Expected 2 sessions, got %d", len(ids))
	}

	// Check both IDs are present
	found1, found2 := false, false
	for _, id := range ids {
		if id == sess1.ID {
			found1 = true
		}
		if id == sess2.ID {
			found2 = true
		}
	}

	if !found1 || !found2 {
		t.Fatalf("Expected IDs %s and %s, got %v", sess1.ID, sess2.ID, ids)
	}
}

func TestIsProcessAlive(t *testing.T) {
	// Current process should be alive
	if !session.IsProcessAlive(os.Getpid()) {
		t.Fatal("Current process should be alive")
	}

	// Invalid PID should not be alive
	if session.IsProcessAlive(0) {
		t.Fatal("PID 0 should not be alive")
	}

	if session.IsProcessAlive(-1) {
		t.Fatal("PID -1 should not be alive")
	}

	// Very high PID unlikely to exist
	if session.IsProcessAlive(999999999) {
		t.Fatal("PID 999999999 should not be alive")
	}
}
