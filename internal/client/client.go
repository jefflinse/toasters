package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// Compile-time assertion: RemoteClient implements service.Service.
var _ service.Service = (*RemoteClient)(nil)

// RemoteClient implements service.Service over HTTP+SSE. It connects to a
// standalone Toasters server and translates REST responses and SSE events
// into service-level types that the TUI can consume.
type RemoteClient struct {
	http    *httpTransport
	baseURL string
	ctx     context.Context
	cancel  context.CancelFunc

	// token is the bearer token for authentication. If empty, no auth header is sent.
	token string

	// connected tracks whether the SSE event stream is currently connected.
	// Set to true when the SSE connection is established, false when it drops.
	connected atomic.Bool

	// Sub-interface implementations.
	operator    *remoteOperatorService
	definitions *remoteDefinitionService
	jobs        *remoteJobService
	sessions    *remoteSessionService
	events      *remoteEventService
	system      *remoteSystemService
}

// Option configures a RemoteClient.
type Option func(*RemoteClient)

// WithHTTPClient sets a custom http.Client for the RemoteClient.
// Use this to configure timeouts, TLS, or custom transports.
func WithHTTPClient(c *http.Client) Option {
	return func(rc *RemoteClient) {
		rc.http.client = c
	}
}

// WithToken sets the bearer token for authentication. All HTTP requests
// and SSE connections will include an Authorization: Bearer header.
func WithToken(token string) Option {
	return func(rc *RemoteClient) {
		rc.token = token
	}
}

// New creates a new RemoteClient connected to the given base URL.
// The base URL must include the scheme (http or https) and host
// (e.g. "http://localhost:8080", "https://example.com").
// Call Close when the client is no longer needed.
func New(baseURL string, opts ...Option) (*RemoteClient, error) {
	// Validate the baseURL has a scheme and host.
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid base URL: scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid base URL: missing host")
	}

	ctx, cancel := context.WithCancel(context.Background())

	rc := &RemoteClient{
		http: &httpTransport{
			client:    &http.Client{Timeout: 30 * time.Second},
			sseClient: &http.Client{},
			baseURL:   baseURL,
		},
		baseURL: baseURL,
		ctx:     ctx,
		cancel:  cancel,
	}

	for _, opt := range opts {
		opt(rc)
	}

	// Propagate token to transport after all options have been applied.
	rc.http.token = rc.token

	rc.operator = &remoteOperatorService{c: rc}
	rc.definitions = &remoteDefinitionService{c: rc}
	rc.jobs = &remoteJobService{c: rc}
	rc.sessions = &remoteSessionService{c: rc}
	rc.events = &remoteEventService{c: rc}
	rc.system = &remoteSystemService{c: rc}

	return rc, nil
}

// Close cancels the client's context, terminating any in-flight SSE
// subscriptions and pending requests.
func (c *RemoteClient) Close() {
	c.cancel()
}

// ---------------------------------------------------------------------------
// Service sub-interface accessors
// ---------------------------------------------------------------------------

// Operator returns the sub-interface for sending messages and managing
// the operator LLM conversation.
func (c *RemoteClient) Operator() service.OperatorService { return c.operator }

// Definitions returns the sub-interface for managing skills, agents, and teams.
func (c *RemoteClient) Definitions() service.DefinitionService { return c.definitions }

// Jobs returns the sub-interface for listing, inspecting, and cancelling jobs.
func (c *RemoteClient) Jobs() service.JobService { return c.jobs }

// Sessions returns the sub-interface for listing and inspecting agent sessions.
func (c *RemoteClient) Sessions() service.SessionService { return c.sessions }

// Events returns the sub-interface for subscribing to the unified event stream.
func (c *RemoteClient) Events() service.EventService { return c.events }

// System returns the sub-interface for health checks, model listing, and
// MCP server status.
func (c *RemoteClient) System() service.SystemService { return c.system }

// ---------------------------------------------------------------------------
// OperatorService
// ---------------------------------------------------------------------------

type remoteOperatorService struct{ c *RemoteClient }

func (s *remoteOperatorService) SendMessage(ctx context.Context, message string) (string, error) {
	resp, err := s.c.http.post(ctx, "/api/v1/operator/messages", struct {
		Message string `json:"message"`
	}{Message: message})
	if err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}
	tr, err := decodeResponse[turnResponse](resp)
	if err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}
	return tr.TurnID, nil
}

func (s *remoteOperatorService) RespondToPrompt(ctx context.Context, requestID string, response string) error {
	resp, err := s.c.http.post(ctx, fmt.Sprintf("/api/v1/operator/prompts/%s/respond", url.PathEscape(requestID)), struct {
		Response string `json:"response"`
	}{Response: response})
	if err != nil {
		return fmt.Errorf("respond to prompt: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("respond to prompt: %w", err)
	}
	return nil
}

func (s *remoteOperatorService) Status(ctx context.Context) (service.OperatorStatus, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/operator/status")
	if err != nil {
		return service.OperatorStatus{}, fmt.Errorf("get operator status: %w", err)
	}
	w, err := decodeResponse[operatorStatusResponse](resp)
	if err != nil {
		return service.OperatorStatus{}, fmt.Errorf("get operator status: %w", err)
	}
	return service.OperatorStatus{
		State:         service.OperatorState(w.State),
		CurrentTurnID: w.CurrentTurnID,
		ModelName:     w.ModelName,
		Endpoint:      w.Endpoint,
	}, nil
}

func (s *remoteOperatorService) History(ctx context.Context) ([]service.ChatEntry, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/operator/history")
	if err != nil {
		return nil, fmt.Errorf("get operator history: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireChatEntry]](resp)
	if err != nil {
		return nil, fmt.Errorf("get operator history: %w", err)
	}
	entries := make([]service.ChatEntry, 0, len(pr.Items))
	for _, w := range pr.Items {
		entries = append(entries, wireChatEntryToService(w))
	}
	return entries, nil
}

func (s *remoteOperatorService) RespondToBlocker(ctx context.Context, jobID string, taskID string, answers []string) error {
	resp, err := s.c.http.post(ctx, fmt.Sprintf("/api/v1/operator/blockers/%s/%s/respond", url.PathEscape(jobID), url.PathEscape(taskID)), struct {
		Answers []string `json:"answers"`
	}{Answers: answers})
	if err != nil {
		return fmt.Errorf("respond to blocker: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("respond to blocker: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// DefinitionService
// ---------------------------------------------------------------------------

type remoteDefinitionService struct{ c *RemoteClient }

// --- Skills ---

func (s *remoteDefinitionService) ListSkills(ctx context.Context) ([]service.Skill, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/skills")
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireSkill]](resp)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	skills := make([]service.Skill, 0, len(pr.Items))
	for _, w := range pr.Items {
		skills = append(skills, wireSkillToService(w))
	}
	return skills, nil
}

func (s *remoteDefinitionService) GetSkill(ctx context.Context, id string) (service.Skill, error) {
	resp, err := s.c.http.get(ctx, fmt.Sprintf("/api/v1/skills/%s", url.PathEscape(id)))
	if err != nil {
		return service.Skill{}, fmt.Errorf("get skill: %w", err)
	}
	w, err := decodeResponse[wireSkill](resp)
	if err != nil {
		return service.Skill{}, fmt.Errorf("get skill: %w", err)
	}
	return wireSkillToService(w), nil
}

func (s *remoteDefinitionService) CreateSkill(ctx context.Context, name string) (service.Skill, error) {
	resp, err := s.c.http.post(ctx, "/api/v1/skills", struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return service.Skill{}, fmt.Errorf("create skill: %w", err)
	}
	w, err := decodeResponse[wireSkill](resp)
	if err != nil {
		return service.Skill{}, fmt.Errorf("create skill: %w", err)
	}
	return wireSkillToService(w), nil
}

func (s *remoteDefinitionService) DeleteSkill(ctx context.Context, id string) error {
	resp, err := s.c.http.delete(ctx, fmt.Sprintf("/api/v1/skills/%s", url.PathEscape(id)))
	if err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}
	return nil
}

func (s *remoteDefinitionService) GenerateSkill(ctx context.Context, prompt string) (string, error) {
	resp, err := s.c.http.post(ctx, "/api/v1/skills/generate", struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt})
	if err != nil {
		return "", fmt.Errorf("generate skill: %w", err)
	}
	ar, err := decodeResponse[asyncResponse](resp)
	if err != nil {
		return "", fmt.Errorf("generate skill: %w", err)
	}
	return ar.OperationID, nil
}

// --- Agents ---

func (s *remoteDefinitionService) ListAgents(ctx context.Context) ([]service.Agent, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/agents")
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireAgent]](resp)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	agents := make([]service.Agent, 0, len(pr.Items))
	for _, w := range pr.Items {
		agents = append(agents, wireAgentToService(w))
	}
	return agents, nil
}

func (s *remoteDefinitionService) GetAgent(ctx context.Context, id string) (service.Agent, error) {
	resp, err := s.c.http.get(ctx, fmt.Sprintf("/api/v1/agents/%s", url.PathEscape(id)))
	if err != nil {
		return service.Agent{}, fmt.Errorf("get agent: %w", err)
	}
	w, err := decodeResponse[wireAgent](resp)
	if err != nil {
		return service.Agent{}, fmt.Errorf("get agent: %w", err)
	}
	return wireAgentToService(w), nil
}

func (s *remoteDefinitionService) CreateAgent(ctx context.Context, name string) (service.Agent, error) {
	resp, err := s.c.http.post(ctx, "/api/v1/agents", struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return service.Agent{}, fmt.Errorf("create agent: %w", err)
	}
	w, err := decodeResponse[wireAgent](resp)
	if err != nil {
		return service.Agent{}, fmt.Errorf("create agent: %w", err)
	}
	return wireAgentToService(w), nil
}

func (s *remoteDefinitionService) DeleteAgent(ctx context.Context, id string) error {
	resp, err := s.c.http.delete(ctx, fmt.Sprintf("/api/v1/agents/%s", url.PathEscape(id)))
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	return nil
}

func (s *remoteDefinitionService) AddSkillToAgent(ctx context.Context, agentID string, skillName string) error {
	resp, err := s.c.http.post(ctx, fmt.Sprintf("/api/v1/agents/%s/skills", url.PathEscape(agentID)), struct {
		SkillName string `json:"skill_name"`
	}{SkillName: skillName})
	if err != nil {
		return fmt.Errorf("add skill to agent: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("add skill to agent: %w", err)
	}
	return nil
}

func (s *remoteDefinitionService) GenerateAgent(ctx context.Context, prompt string) (string, error) {
	resp, err := s.c.http.post(ctx, "/api/v1/agents/generate", struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt})
	if err != nil {
		return "", fmt.Errorf("generate agent: %w", err)
	}
	ar, err := decodeResponse[asyncResponse](resp)
	if err != nil {
		return "", fmt.Errorf("generate agent: %w", err)
	}
	return ar.OperationID, nil
}

// --- Teams ---

func (s *remoteDefinitionService) ListTeams(ctx context.Context) ([]service.TeamView, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/teams")
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireTeamView]](resp)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	teams := make([]service.TeamView, 0, len(pr.Items))
	for _, w := range pr.Items {
		teams = append(teams, wireTeamViewToService(w))
	}
	return teams, nil
}

func (s *remoteDefinitionService) GetTeam(ctx context.Context, id string) (service.TeamView, error) {
	resp, err := s.c.http.get(ctx, fmt.Sprintf("/api/v1/teams/%s", url.PathEscape(id)))
	if err != nil {
		return service.TeamView{}, fmt.Errorf("get team: %w", err)
	}
	w, err := decodeResponse[wireTeamView](resp)
	if err != nil {
		return service.TeamView{}, fmt.Errorf("get team: %w", err)
	}
	return wireTeamViewToService(w), nil
}

func (s *remoteDefinitionService) CreateTeam(ctx context.Context, name string) (service.TeamView, error) {
	resp, err := s.c.http.post(ctx, "/api/v1/teams", struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return service.TeamView{}, fmt.Errorf("create team: %w", err)
	}
	w, err := decodeResponse[wireTeamView](resp)
	if err != nil {
		return service.TeamView{}, fmt.Errorf("create team: %w", err)
	}
	return wireTeamViewToService(w), nil
}

func (s *remoteDefinitionService) DeleteTeam(ctx context.Context, id string) error {
	resp, err := s.c.http.delete(ctx, fmt.Sprintf("/api/v1/teams/%s", url.PathEscape(id)))
	if err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	return nil
}

func (s *remoteDefinitionService) AddAgentToTeam(ctx context.Context, teamID string, agentID string) error {
	resp, err := s.c.http.post(ctx, fmt.Sprintf("/api/v1/teams/%s/agents", url.PathEscape(teamID)), struct {
		AgentID string `json:"agent_id"`
	}{AgentID: agentID})
	if err != nil {
		return fmt.Errorf("add agent to team: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("add agent to team: %w", err)
	}
	return nil
}

func (s *remoteDefinitionService) SetCoordinator(ctx context.Context, teamID string, agentName string) error {
	resp, err := s.c.http.put(ctx, fmt.Sprintf("/api/v1/teams/%s/coordinator", url.PathEscape(teamID)), struct {
		AgentName string `json:"agent_name"`
	}{AgentName: agentName})
	if err != nil {
		return fmt.Errorf("set coordinator: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("set coordinator: %w", err)
	}
	return nil
}

func (s *remoteDefinitionService) PromoteTeam(ctx context.Context, teamID string) (string, error) {
	resp, err := s.c.http.post(ctx, fmt.Sprintf("/api/v1/teams/%s/promote", url.PathEscape(teamID)), nil)
	if err != nil {
		return "", fmt.Errorf("promote team: %w", err)
	}
	ar, err := decodeResponse[asyncResponse](resp)
	if err != nil {
		return "", fmt.Errorf("promote team: %w", err)
	}
	return ar.OperationID, nil
}

func (s *remoteDefinitionService) GenerateTeam(ctx context.Context, prompt string) (string, error) {
	resp, err := s.c.http.post(ctx, "/api/v1/teams/generate", struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt})
	if err != nil {
		return "", fmt.Errorf("generate team: %w", err)
	}
	ar, err := decodeResponse[asyncResponse](resp)
	if err != nil {
		return "", fmt.Errorf("generate team: %w", err)
	}
	return ar.OperationID, nil
}

func (s *remoteDefinitionService) DetectCoordinator(ctx context.Context, teamID string) (string, error) {
	resp, err := s.c.http.post(ctx, fmt.Sprintf("/api/v1/teams/%s/detect-coordinator", url.PathEscape(teamID)), nil)
	if err != nil {
		return "", fmt.Errorf("detect coordinator: %w", err)
	}
	ar, err := decodeResponse[asyncResponse](resp)
	if err != nil {
		return "", fmt.Errorf("detect coordinator: %w", err)
	}
	return ar.OperationID, nil
}

// ---------------------------------------------------------------------------
// JobService
// ---------------------------------------------------------------------------

type remoteJobService struct{ c *RemoteClient }

func (s *remoteJobService) List(ctx context.Context, filter *service.JobListFilter) ([]service.Job, error) {
	path := "/api/v1/jobs"
	if filter != nil {
		v := url.Values{}
		if filter.Status != nil {
			v.Set("status", string(*filter.Status))
		}
		if filter.Type != nil {
			v.Set("type", *filter.Type)
		}
		if filter.Limit > 0 {
			v.Set("limit", strconv.Itoa(filter.Limit))
		}
		if filter.Offset > 0 {
			v.Set("offset", strconv.Itoa(filter.Offset))
		}
		if q := v.Encode(); q != "" {
			path += "?" + q
		}
	}
	resp, err := s.c.http.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireJob]](resp)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	jobs := make([]service.Job, 0, len(pr.Items))
	for _, w := range pr.Items {
		jobs = append(jobs, wireJobToService(w))
	}
	return jobs, nil
}

func (s *remoteJobService) ListAll(ctx context.Context) ([]service.Job, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/jobs?all=true")
	if err != nil {
		return nil, fmt.Errorf("list all jobs: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireJob]](resp)
	if err != nil {
		return nil, fmt.Errorf("list all jobs: %w", err)
	}
	jobs := make([]service.Job, 0, len(pr.Items))
	for _, w := range pr.Items {
		jobs = append(jobs, wireJobToService(w))
	}
	return jobs, nil
}

func (s *remoteJobService) Get(ctx context.Context, id string) (service.JobDetail, error) {
	resp, err := s.c.http.get(ctx, fmt.Sprintf("/api/v1/jobs/%s", url.PathEscape(id)))
	if err != nil {
		return service.JobDetail{}, fmt.Errorf("get job: %w", err)
	}
	w, err := decodeResponse[wireJobDetail](resp)
	if err != nil {
		return service.JobDetail{}, fmt.Errorf("get job: %w", err)
	}
	return wireJobDetailToService(w), nil
}

func (s *remoteJobService) Cancel(ctx context.Context, id string) error {
	resp, err := s.c.http.post(ctx, fmt.Sprintf("/api/v1/jobs/%s/cancel", url.PathEscape(id)), nil)
	if err != nil {
		return fmt.Errorf("cancel job: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("cancel job: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SessionService
// ---------------------------------------------------------------------------

type remoteSessionService struct{ c *RemoteClient }

func (s *remoteSessionService) List(ctx context.Context) ([]service.SessionSnapshot, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/sessions")
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireSessionSnapshot]](resp)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	sessions := make([]service.SessionSnapshot, 0, len(pr.Items))
	for _, w := range pr.Items {
		sessions = append(sessions, wireSessionSnapshotToService(w))
	}
	return sessions, nil
}

func (s *remoteSessionService) Get(ctx context.Context, id string) (service.SessionDetail, error) {
	resp, err := s.c.http.get(ctx, fmt.Sprintf("/api/v1/sessions/%s", url.PathEscape(id)))
	if err != nil {
		return service.SessionDetail{}, fmt.Errorf("get session: %w", err)
	}
	w, err := decodeResponse[wireSessionDetail](resp)
	if err != nil {
		return service.SessionDetail{}, fmt.Errorf("get session: %w", err)
	}
	return wireSessionDetailToService(w), nil
}

func (s *remoteSessionService) Cancel(ctx context.Context, id string) error {
	resp, err := s.c.http.post(ctx, fmt.Sprintf("/api/v1/sessions/%s/cancel", url.PathEscape(id)), nil)
	if err != nil {
		return fmt.Errorf("cancel session: %w", err)
	}
	if err := decodeNoContent(resp); err != nil {
		return fmt.Errorf("cancel session: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SystemService
// ---------------------------------------------------------------------------

type remoteSystemService struct{ c *RemoteClient }

func (s *remoteSystemService) Health(ctx context.Context) (service.HealthStatus, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/health")
	if err != nil {
		return service.HealthStatus{}, fmt.Errorf("health check: %w", err)
	}
	w, err := decodeResponse[healthResponse](resp)
	if err != nil {
		return service.HealthStatus{}, fmt.Errorf("health check: %w", err)
	}
	return service.HealthStatus{
		Status:  w.Status,
		Version: w.Version,
		Uptime:  time.Duration(w.UptimeSeconds * float64(time.Second)),
	}, nil
}

func (s *remoteSystemService) ListModels(ctx context.Context) ([]service.ModelInfo, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/models")
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireModelInfo]](resp)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	models := make([]service.ModelInfo, 0, len(pr.Items))
	for _, w := range pr.Items {
		models = append(models, wireModelInfoToService(w))
	}
	return models, nil
}

func (s *remoteSystemService) ListCatalogProviders(ctx context.Context) ([]service.CatalogProvider, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/catalog")
	if err != nil {
		return nil, fmt.Errorf("list catalog: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireCatalogProvider]](resp)
	if err != nil {
		return nil, fmt.Errorf("list catalog: %w", err)
	}
	providers := make([]service.CatalogProvider, 0, len(pr.Items))
	for _, w := range pr.Items {
		providers = append(providers, wireCatalogProviderToService(w))
	}
	return providers, nil
}

func (s *remoteSystemService) AddProvider(ctx context.Context, req service.AddProviderRequest) error {
	resp, err := s.c.http.post(ctx, "/api/v1/providers", wireAddProviderRequest{
		ID:       req.ID,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		APIKey:   req.APIKey,
	})
	if err != nil {
		return fmt.Errorf("add provider: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		return fmt.Errorf("add provider: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (s *remoteSystemService) UpdateProvider(ctx context.Context, req service.AddProviderRequest) error {
	resp, err := s.c.http.put(ctx, "/api/v1/providers", wireAddProviderRequest{
		ID:       req.ID,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		APIKey:   req.APIKey,
	})
	if err != nil {
		return fmt.Errorf("update provider: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("update provider: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (s *remoteSystemService) ListConfiguredProviderIDs(ctx context.Context) ([]string, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/providers/configured")
	if err != nil {
		return nil, fmt.Errorf("list configured providers: %w", err)
	}
	ids, err := decodeResponse[[]string](resp)
	if err != nil {
		return nil, fmt.Errorf("list configured providers: %w", err)
	}
	return ids, nil
}

func (s *remoteSystemService) ListMCPServers(ctx context.Context) ([]service.MCPServerStatus, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/mcp/servers")
	if err != nil {
		return nil, fmt.Errorf("list MCP servers: %w", err)
	}
	pr, err := decodeResponse[paginatedResponse[wireMCPServerStatus]](resp)
	if err != nil {
		return nil, fmt.Errorf("list MCP servers: %w", err)
	}
	servers := make([]service.MCPServerStatus, 0, len(pr.Items))
	for _, w := range pr.Items {
		servers = append(servers, wireMCPServerStatusToService(w))
	}
	return servers, nil
}

func (s *remoteSystemService) GetProgressState(ctx context.Context) (service.ProgressState, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/progress")
	if err != nil {
		return service.ProgressState{}, fmt.Errorf("get progress state: %w", err)
	}
	w, err := decodeResponse[wireProgressState](resp)
	if err != nil {
		return service.ProgressState{}, fmt.Errorf("get progress state: %w", err)
	}
	return wireProgressStateToService(w), nil
}

func (s *remoteSystemService) GetLogs(ctx context.Context) (string, error) {
	resp, err := s.c.http.get(ctx, "/api/v1/logs")
	if err != nil {
		return "", fmt.Errorf("get logs: %w", err)
	}
	w, err := decodeResponse[logsResponse](resp)
	if err != nil {
		return "", fmt.Errorf("get logs: %w", err)
	}
	return w.Content, nil
}
