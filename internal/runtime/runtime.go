package runtime

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

// Runtime manages agent sessions.
type Runtime struct {
	mu        sync.Mutex
	sessions  map[string]*Session
	store     db.Store // may be nil
	providers *provider.Registry
	mcpCaller MCPCaller // may be nil
	mcpDefs   []ToolDef // pre-converted MCP tool definitions
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
// IMPORTANT: This must be called before any calls to SpawnAgent. It is not
// safe to call concurrently with SpawnAgent.
func (r *Runtime) SetMCPCaller(caller MCPCaller, defs []ToolDef) {
	r.mcpCaller = caller
	r.mcpDefs = defs
}

// SpawnAgent creates and starts a new agent session.
func (r *Runtime) SpawnAgent(ctx context.Context, opts SpawnOpts) (*Session, error) {
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

	// Create tool executor with core tools, optionally wrapping with MCP dispatch.
	coreTools := NewCoreTools(
		opts.WorkDir,
		WithShell(true),
		WithSpawner(r, depth+1, maxDepth),
		WithStore(r.store),
		WithSessionContext(id, opts.AgentID, opts.JobID),
	)
	var tools ToolExecutor
	if r.mcpCaller != nil {
		tools = NewCompositeTools(coreTools, r.mcpCaller, r.mcpDefs)
	} else {
		tools = coreTools
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
			log.Printf("warning: failed to persist session %s: %v", id, err)
		}
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
				log.Printf("warning: failed to update session %s: %v", id, updateErr)
			}
		}

		if err != nil && err != context.Canceled {
			log.Printf("session %s ended with error: %v", id, err)
		}
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
		return "", fmt.Errorf("child session failed")
	}

	return sess.FinalText(), nil
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
