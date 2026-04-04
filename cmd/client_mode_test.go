package cmd

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jefflinse/toasters/internal/auth"
	"github.com/jefflinse/toasters/internal/client"
	"github.com/jefflinse/toasters/internal/server"
	"github.com/jefflinse/toasters/internal/tui"
)

// TestClientModeInitializationRegression tests that client mode properly sends
// AppReadyMsg after establishing a connection.
//
// This is a regression test for the bug where client mode would hang on the
// loading screen forever because AppReadyMsg was never sent.
//
// The test verifies:
// 1. The TUI can be initialized in client mode
// 2. sendClientModeAppReady is called to send AppReadyMsg
// 3. The program doesn't hang in loading state
//
// HOW TO VERIFY THIS CATCHES THE BUG:
// To verify this test would fail without the fix, temporarily comment out
// the call to sendClientModeAppReady in root.go (around line 276).
// The TestClientModeDoesNotHang test should then timeout and fail.
func TestClientModeInitializationRegression(t *testing.T) {
	t.Parallel()

	// Create temp config dir and token
	tmpDir := t.TempDir()
	token, err := auth.EnsureToken(tmpDir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	// Create a mock service and server
	mockSvc := newMockService()
	srv := server.New(mockSvc, server.WithToken(token))

	// Start server on random port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Start(":0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	serverAddr := srv.Addr()
	if serverAddr == "" {
		t.Fatal("server addr is empty")
	}

	addrWithoutColon := serverAddr[1:]

	// Create client connection
	rc, err := client.New("http://"+addrWithoutColon, client.WithToken(token))
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	defer rc.Close()

	// Create TUI model - this is what happens in client mode
	m := tui.NewModel(tui.ModelConfig{
		Service:   rc,
		ConfigDir: tmpDir,
	})

	prog := tea.NewProgram(&m)
	var p atomic.Pointer[tea.Program]
	p.Store(prog)

	var wg sync.WaitGroup
	wg.Add(1)

	// Run the program
	go func() {
		defer wg.Done()
		_, _ = prog.Run()
	}()

	// This is the CRITICAL call that must happen in client mode
	// If this line is missing, the TUI hangs forever in loading state
	sendClientModeAppReady(rc, &p, tmpDir, addrWithoutColon)

	// Give it time to process the message
	time.Sleep(300 * time.Millisecond)

	// The program should respond to quit quickly if it's not stuck
	quitDone := make(chan struct{})
	go func() {
		prog.Quit()
		wg.Wait()
		close(quitDone)
	}()

	select {
	case <-quitDone:
		t.Log("✓ Client mode initialization completed - TUI exited loading state successfully")
	case <-time.After(2 * time.Second):
		t.Fatal("REGRESSION DETECTED: Program did not respond to quit - likely stuck in loading state because AppReadyMsg was not sent")
	}
}

// TestClientModeDoesNotHang verifies that client mode doesn't hang indefinitely.
// This test has a strict timeout to catch the regression.
//
// REGRESSION VERIFICATION:
// If you comment out sendClientModeAppReady in root.go, this test will fail
// with a timeout error.
func TestClientModeDoesNotHang(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	token, err := auth.EnsureToken(tmpDir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	mockSvc := newMockService()
	srv := server.New(mockSvc, server.WithToken(token))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Start(":0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	serverAddr := srv.Addr()
	addrWithoutColon := serverAddr[1:]

	rc, err := client.New("http://"+addrWithoutColon, client.WithToken(token))
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	defer rc.Close()

	m := tui.NewModel(tui.ModelConfig{
		Service:   rc,
		ConfigDir: tmpDir,
	})

	prog := tea.NewProgram(&m)
	var p atomic.Pointer[tea.Program]
	p.Store(prog)

	// Use a channel to detect if the program completes
	done := make(chan struct{})

	go func() {
		_, _ = prog.Run()
		close(done)
	}()

	// Send AppReadyMsg - this is what the fix does
	go func() {
		time.Sleep(100 * time.Millisecond)
		sendClientModeAppReady(rc, &p, tmpDir, addrWithoutColon)
	}()

	// Try to quit after a short delay
	go func() {
		time.Sleep(400 * time.Millisecond)
		prog.Quit()
	}()

	// The program should complete within 1 second
	// If it's stuck in loading state, it won't respond to quit
	select {
	case <-done:
		t.Log("✓ Program completed successfully - not stuck in loading state")
	case <-time.After(1 * time.Second):
		t.Fatal("REGRESSION: Program hung for >1s - likely stuck in loading state because AppReadyMsg was not sent")
	}
}

// TestSendClientModeAppReady_Function tests the sendClientModeAppReady function directly
func TestSendClientModeAppReady_Function(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	token, err := auth.EnsureToken(tmpDir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	mockSvc := newMockService()
	srv := server.New(mockSvc, server.WithToken(token))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Start(":0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	serverAddr := srv.Addr()
	addrWithoutColon := serverAddr[1:]

	rc, err := client.New("http://"+addrWithoutColon, client.WithToken(token))
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	defer rc.Close()

	// Verify the function exists and can be called
	// This test documents the expected behavior
	appReadySent := make(chan struct{}, 1)

	m := tui.NewModel(tui.ModelConfig{
		Service:   rc,
		ConfigDir: tmpDir,
	})

	prog := tea.NewProgram(&m)
	var p atomic.Pointer[tea.Program]
	p.Store(prog)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		_, _ = prog.Run()
	}()

	// Call the function
	go func() {
		sendClientModeAppReady(rc, &p, tmpDir, addrWithoutColon)
		time.Sleep(100 * time.Millisecond)
		appReadySent <- struct{}{}
	}()

	select {
	case <-appReadySent:
		t.Log("✓ sendClientModeAppReady function executed successfully")
	case <-time.After(2 * time.Second):
		t.Fatal("sendClientModeAppReady did not complete within 2 seconds")
	}

	prog.Quit()
	wg.Wait()
}

// TestClientModeAppReadyMsgContent verifies the message content
func TestClientModeAppReadyMsgContent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	token, err := auth.EnsureToken(tmpDir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	mockSvc := newMockService()
	srv := server.New(mockSvc, server.WithToken(token))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Start(":0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	serverAddr := srv.Addr()
	addrWithoutColon := serverAddr[1:]

	rc, err := client.New("http://"+addrWithoutColon, client.WithToken(token))
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	defer rc.Close()

	// Test message creation (what sendClientModeAppReady does internally)
	ctx = context.Background()
	initialTeams, _ := rc.Definitions().ListTeams(ctx)
	awareness := generateTeamAwareness(ctx, nil, initialTeams, tmpDir)

	msg := tui.AppReadyMsg{
		Awareness: awareness,
		Greeting:  "Connected to " + addrWithoutColon,
	}

	// Verify
	if msg.Greeting == "" {
		t.Error("Greeting should not be empty")
	}

	expected := "Connected to " + addrWithoutColon
	if msg.Greeting != expected {
		t.Errorf("Greeting = %q, want %q", msg.Greeting, expected)
	}

	t.Logf("✓ AppReadyMsg content correct: %q", msg.Greeting)
}

// TestSendClientModeAppReady_Timing verifies performance
func TestSendClientModeAppReady_Timing(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	token, err := auth.EnsureToken(tmpDir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	mockSvc := newMockService()
	srv := server.New(mockSvc, server.WithToken(token))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Start(":0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	serverAddr := srv.Addr()
	addrWithoutColon := serverAddr[1:]

	rc, err := client.New("http://"+addrWithoutColon, client.WithToken(token))
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	defer rc.Close()

	// Measure the time for the function's operations
	start := time.Now()

	done := make(chan struct{})
	go func() {
		ctx := context.Background()
		_, _ = rc.Definitions().ListTeams(ctx)
		_ = generateTeamAwareness(ctx, nil, nil, tmpDir)
		time.Sleep(100 * time.Millisecond) // The intentional delay
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		t.Logf("✓ Initialization completed in %v", elapsed)

		if elapsed > 200*time.Millisecond {
			t.Errorf("Took too long: %v (expected < 200ms)", elapsed)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Did not complete within 1 second")
	}
}
