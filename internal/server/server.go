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
	"sync/atomic"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// Server wraps a service.Service and exposes it over HTTP.
type Server struct {
	svc       service.Service
	httpSrv   *http.Server
	logger    *slog.Logger
	startTime time.Time
	sseConns  atomic.Int32 // current SSE connection count
}

// Option configures a Server.
type Option func(*Server)

// WithLogger sets the logger for the server.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) {
		s.logger = logger
	}
}

// New creates a new Server wrapping the given service.
func New(svc service.Service, opts ...Option) *Server {
	s := &Server{
		svc:       svc,
		logger:    slog.Default(),
		startTime: time.Now(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start begins listening on the given address. It launches the HTTP server
// in a background goroutine and returns immediately. Use Shutdown to stop.
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Build middleware stack: Recovery → Request ID → Logging → CORS → Content-Type.
	middleware := chain(
		recoveryMiddleware,
		requestIDMiddleware,
		loggingMiddleware,
		corsMiddleware,
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
	if s.httpSrv == nil {
		return nil
	}
	s.logger.Info("server shutting down")
	return s.httpSrv.Shutdown(ctx)
}

// Addr returns the address the server is listening on, or empty string if
// not started.
func (s *Server) Addr() string {
	if s.httpSrv == nil {
		return ""
	}
	return s.httpSrv.Addr
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
	mux.HandleFunc("GET /api/v1/models", s.listModels)
	mux.HandleFunc("GET /api/v1/mcp/servers", s.listMCPServers)
	mux.HandleFunc("GET /api/v1/progress", s.getProgress)

	// SSE
	mux.HandleFunc("GET /api/v1/events", s.events)
}
