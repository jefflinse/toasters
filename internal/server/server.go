// Package server provides the HTTP server for the Toasters REST API.
// It wraps a service.Service over HTTP with SSE event streaming, exposing
// all service methods as REST endpoints per the API specification.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// sseConn represents an active SSE connection for tracking purposes.
type sseConn struct {
	cancel context.CancelFunc
}

// Server wraps a service.Service and exposes it over HTTP.
type Server struct {
	mu        sync.Mutex // guards httpSrv and listener
	svc       service.Service
	httpSrv   *http.Server
	listener  net.Listener
	logger    *slog.Logger
	startTime time.Time
	sseConns  atomic.Int32 // current SSE connection count
	token     string       // bearer token; empty means auth disabled

	// sseConnTracker tracks active SSE connections for graceful shutdown.
	sseConnTracker struct {
		mu    sync.Mutex
		conns map[*sseConn]struct{}
	}
}

// Option configures a Server.
type Option func(*Server)

// WithLogger sets the logger for the server.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) {
		s.logger = logger
	}
}

// WithToken sets the bearer token for the server. When non-empty, all requests
// except GET /api/v1/health must supply a matching Authorization: Bearer header.
// Pass an empty string (or omit this option) to disable authentication.
func WithToken(token string) Option {
	return func(s *Server) {
		s.token = token
	}
}

// New creates a new Server wrapping the given service.
func New(svc service.Service, opts ...Option) *Server {
	s := &Server{
		svc:       svc,
		logger:    slog.Default(),
		startTime: time.Now(),
	}
	s.sseConnTracker.conns = make(map[*sseConn]struct{})
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start begins listening on the given address. It launches the HTTP server
// in a background goroutine and returns immediately. Use Shutdown to stop.
func (s *Server) Start(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Build middleware stack: Recovery → Request ID → Auth → Logging → CORS → Security Headers → Content-Type.
	middleware := chain(
		recoveryMiddleware,
		requestIDMiddleware,
		authMiddleware(s.token),
		loggingMiddleware,
		corsMiddleware,
		securityHeadersMiddleware,
		contentTypeMiddleware,
	)

	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	s.listener = ln

	s.logger.Info("server starting", "addr", ln.Addr().String())

	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Error("server error", "error", err)
		}
	}()

	return nil
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpSrv == nil {
		return nil
	}
	s.logger.Info("server shutting down")
	return s.httpSrv.Shutdown(ctx)
}

// CloseAllSSEConnections force-closes all active SSE connections by cancelling
// their contexts. This should be called before Shutdown to ensure blocked SSE
// writes can complete quickly.
func (s *Server) CloseAllSSEConnections() {
	s.sseConnTracker.mu.Lock()
	defer s.sseConnTracker.mu.Unlock()

	for conn := range s.sseConnTracker.conns {
		conn.cancel()
	}
}

// Addr returns the address the server is listening on, or empty string if
// not started. For servers bound on :0, this returns the actual resolved port.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// registerRoutes registers all API routes on the given mux.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Operator
	mux.HandleFunc("POST /api/v1/operator/messages", s.sendMessage)
	mux.HandleFunc("POST /api/v1/operator/prompts/{requestId}/respond", s.respondToPrompt)
	mux.HandleFunc("GET /api/v1/operator/status", s.operatorStatus)
	mux.HandleFunc("GET /api/v1/operator/history", s.operatorHistory)
	mux.HandleFunc("POST /api/v1/operator/blockers/{jobId}/{taskId}/respond", s.respondToBlocker)

	// Skills
	mux.HandleFunc("GET /api/v1/skills", s.listSkills)
	mux.HandleFunc("GET /api/v1/skills/{id}", s.getSkill)
	mux.HandleFunc("POST /api/v1/skills", s.createSkill)
	mux.HandleFunc("DELETE /api/v1/skills/{id}", s.deleteSkill)
	mux.HandleFunc("POST /api/v1/skills/generate", s.generateSkill)

	// Agents
	mux.HandleFunc("GET /api/v1/agents", s.listAgents)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.getAgent)
	mux.HandleFunc("POST /api/v1/agents", s.createAgent)
	mux.HandleFunc("DELETE /api/v1/agents/{id}", s.deleteAgent)
	mux.HandleFunc("POST /api/v1/agents/{id}/skills", s.addSkillToAgent)
	mux.HandleFunc("POST /api/v1/agents/generate", s.generateAgent)

	// Teams
	mux.HandleFunc("GET /api/v1/teams", s.listTeams)
	mux.HandleFunc("GET /api/v1/teams/{id}", s.getTeam)
	mux.HandleFunc("POST /api/v1/teams", s.createTeam)
	mux.HandleFunc("DELETE /api/v1/teams/{id}", s.deleteTeam)
	mux.HandleFunc("POST /api/v1/teams/{id}/agents", s.addAgentToTeam)
	mux.HandleFunc("PUT /api/v1/teams/{id}/coordinator", s.setCoordinator)
	mux.HandleFunc("POST /api/v1/teams/{id}/promote", s.promoteTeam)
	mux.HandleFunc("POST /api/v1/teams/generate", s.generateTeam)
	mux.HandleFunc("POST /api/v1/teams/{id}/detect-coordinator", s.detectCoordinator)

	// Jobs
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.cancelJob)

	// Sessions
	mux.HandleFunc("GET /api/v1/sessions", s.listSessions)
	mux.HandleFunc("GET /api/v1/sessions/{id}", s.getSession)
	mux.HandleFunc("POST /api/v1/sessions/{id}/cancel", s.cancelSession)

	// System
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/logs", s.getLogs)
	mux.HandleFunc("GET /api/v1/models", s.listModels)
	mux.HandleFunc("GET /api/v1/catalog", s.listCatalog)
	mux.HandleFunc("POST /api/v1/providers", s.addProvider)
	mux.HandleFunc("GET /api/v1/mcp/servers", s.listMCPServers)
	mux.HandleFunc("GET /api/v1/progress", s.getProgress)

	// SSE
	mux.HandleFunc("GET /api/v1/events", s.events)
}
