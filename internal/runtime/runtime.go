package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

// Runtime manages agent sessions.
type Runtime struct {
	mu               sync.Mutex
	sessions         map[string]*Session
	store            db.Store // may be nil
	providers        *provider.Registry
	mcpCaller        MCPCaller      // may be nil
	mcpDefs          []ToolDef      // pre-converted MCP tool definitions
	OnSessionStarted func(*Session) // called after each SpawnAgent; may be nil
}

// New creates a new Runtime. store may be nil for in-memory only operation.
func New(store db.Store, providers *provider.Registry) *Runtime {
	return &Runtime{
		sessions:  make(map[string]*Session),
		store:     store,
		providers: providers,
	}
}

// SetMCPCaller wires an MCP caller into the runtime for agent tool dispatch.
// mcpDefs are the pre-converted tool definitions in runtime.ToolDef format.
// Safe to call concurrently with SpawnAgent.
func (r *Runtime) SetMCPCaller(caller MCPCaller, defs []ToolDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mcpCaller = caller
	r.mcpDefs = defs
}

// SpawnAgent creates and starts a new agent session.
func (r *Runtime) SpawnAgent(ctx context.Context, opts SpawnOpts) (*Session, error) {
	// Validate mutually exclusive options.
	if opts.ToolExecutor != nil && opts.ExtraTools != nil {
		return nil, fmt.Errorf("SpawnOpts.ToolExecutor and SpawnOpts.ExtraTools are mutually exclusive")
	}

	// Look up provider from registry.
	p, ok := r.providers.Get(opts.ProviderName)
	if !ok {
		return nil, fmt.Errorf("provider %q not found", opts.ProviderName)
	}

	id := uuid.New().String()

	// Determine spawn depth for child agents.
	depth := opts.Depth
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}

	// Snapshot MCP state under lock for use below.
	r.mu.Lock()
	mcpCaller := r.mcpCaller
	mcpDefs := r.mcpDefs
	r.mu.Unlock()

	// Create tool executor. If the caller provided a custom ToolExecutor, use
	// it directly (e.g. SystemTools for system agents). Otherwise build the
	// default CoreTools stack with optional MCP dispatch.
	var tools ToolExecutor
	if opts.ToolExecutor != nil {
		tools = opts.ToolExecutor
	} else {
		coreTools := NewCoreTools(
			opts.WorkDir,
			WithShell(true),
			WithSpawner(r, depth, maxDepth),
			WithStore(r.store),
			WithSessionContext(id, opts.AgentID, opts.JobID, opts.TaskID),
			WithProvider(opts.ProviderName, opts.Model),
		)
		if mcpCaller != nil {
			tools = NewCompositeTools(coreTools, mcpCaller, mcpDefs)
		} else {
			tools = coreTools
		}
	}

	// If the caller provided extra tools, layer them on top of the base tools
	// so they get dispatch priority.
	if opts.ExtraTools != nil {
		tools = NewLayeredToolExecutor(tools, opts.ExtraTools)
	}

	// If the caller requested a specific tool subset, wrap the executor so that
	// Definitions() returns only those tools. Execute() still dispatches all
	// calls, but the LLM will only know about — and therefore only call — the
	// filtered set.
	if len(opts.Tools) > 0 {
		tools = &filteredToolExecutor{inner: tools, allowed: opts.Tools}
	}

	sess := newSession(id, p, opts, tools)

	// Register in sessions map.
	r.mu.Lock()
	r.sessions[id] = sess
	r.mu.Unlock()

	// Persist to SQLite if store is available.
	if r.store != nil {
		dbSession := &db.AgentSession{
			ID:        id,
			AgentID:   opts.AgentID,
			JobID:     opts.JobID,
			TaskID:    opts.TaskID,
			Status:    db.SessionStatusActive,
			Model:     opts.Model,
			Provider:  opts.ProviderName,
			StartedAt: sess.startTime,
		}
		if err := r.store.CreateSession(ctx, dbSession); err != nil {
			slog.Warn("failed to persist session", "session", id, "error", err)
		}
	}

	// Notify observer before starting the goroutine so the subscriber is set up
	// before events start flowing. Hidden sessions (e.g. internal system agent
	// calls via consult_agent) skip this to avoid appearing in the TUI.
	if r.OnSessionStarted != nil && !opts.Hidden {
		r.OnSessionStarted(sess)
	}

	// Start session in goroutine. Use context.Background() because the session
	// has its own internal context for lifecycle management. The caller's context
	// should not control the session's lifetime for fire-and-forget spawns.
	go func() {
		err := sess.Run(context.Background())

		// Update persistence on completion.
		if r.store != nil {
			snap := sess.Snapshot()
			now := time.Now()
			status := db.SessionStatus(snap.Status)
			tokensIn := snap.TokensIn
			tokensOut := snap.TokensOut
			update := db.SessionUpdate{
				Status:    &status,
				TokensIn:  &tokensIn,
				TokensOut: &tokensOut,
				EndedAt:   &now,
			}
			if updateErr := r.store.UpdateSession(context.Background(), id, update); updateErr != nil {
				slog.Warn("failed to update session", "session", id, "error", updateErr)
			}
		}

		if err != nil && err != context.Canceled {
			slog.Error("session ended with error", "session", id, "error", err)
		}

		// Remove the completed session from the map to prevent unbounded growth.
		// Immediate removal is safe: all callers that need the session hold a
		// direct *Session pointer (SpawnAndWait, OnSessionStarted callback).
		// GetSession is only used during active session lifetime.
		r.mu.Lock()
		delete(r.sessions, id)
		r.mu.Unlock()
	}()

	return sess, nil
}

// SpawnAndWait creates a session and blocks until it completes. Returns final text.
// Used by spawn_agent tool.
func (r *Runtime) SpawnAndWait(ctx context.Context, opts SpawnOpts) (string, error) {
	sess, err := r.SpawnAgent(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("spawning agent: %w", err)
	}

	// Wait for session to complete.
	select {
	case <-sess.Done():
	case <-ctx.Done():
		sess.Cancel()
		return "", ctx.Err()
	}

	snap := sess.Snapshot()
	if snap.Status == "failed" {
		if termErr := sess.TermErr(); termErr != nil {
			return "", fmt.Errorf("child session failed: %w", termErr)
		}
		return "", fmt.Errorf("child session failed")
	}

	return sess.FinalText(), nil
}

// filteredToolExecutor wraps a ToolExecutor and enforces a tool allowlist.
// Definitions() returns only the permitted subset so the LLM is never told
// about disallowed tools. Execute() enforces the same allowlist at call time,
// returning ErrUnknownTool for any tool not in the permitted set.
type filteredToolExecutor struct {
	inner   ToolExecutor
	allowed []ToolDef
}

func (f *filteredToolExecutor) Definitions() []ToolDef {
	return f.allowed
}

func (f *filteredToolExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	for _, td := range f.allowed {
		if td.Name == name {
			return f.inner.Execute(ctx, name, args)
		}
	}
	return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
}

// GetSession returns a session by ID.
func (r *Runtime) GetSession(id string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	return s, ok
}

// CancelSession cancels a session by ID.
func (r *Runtime) CancelSession(id string) error {
	r.mu.Lock()
	s, ok := r.sessions[id]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	s.Cancel()
	return nil
}

// Shutdown cancels all active sessions and waits for them to complete.
// Call this before closing the database to ensure session cleanup finishes.
func (r *Runtime) Shutdown() {
	r.mu.Lock()
	sessions := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()

	for _, s := range sessions {
		s.Cancel()
	}

	// Wait for all sessions to finish (they remove themselves from the map).
	for {
		r.mu.Lock()
		remaining := len(r.sessions)
		r.mu.Unlock()
		if remaining == 0 {
			return
		}
		// Brief sleep to avoid busy-waiting.
		time.Sleep(10 * time.Millisecond)
	}
}

// ActiveSessions returns snapshots of all sessions with "active" status.
func (r *Runtime) ActiveSessions() []SessionSnapshot {
	r.mu.Lock()
	sessions := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()

	var active []SessionSnapshot
	for _, s := range sessions {
		snap := s.Snapshot()
		if snap.Status == "active" {
			active = append(active, snap)
		}
	}
	return active
}
