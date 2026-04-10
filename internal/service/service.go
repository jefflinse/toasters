package service

import "context"

// ---------------------------------------------------------------------------
// Top-level Service interface
// ---------------------------------------------------------------------------

// Service is the top-level interface for the Toasters orchestration engine.
// It is composed of domain-specific sub-interfaces, each covering a distinct
// area of functionality. The TUI holds a single Service value and calls
// sub-interface methods for all operations.
//
// Two implementations exist:
//   - LocalService: in-process, delegates to db.Store, operator.Operator,
//     runtime.Runtime, mcp.Manager, etc. Used in embedded mode.
//   - RemoteClient: over HTTP + SSE, used when connecting to a standalone
//     toasters server.
//
// The TUI must not import any internal package other than service. All
// business logic lives in the implementation, not the interface.
type Service interface {
	// Operator returns the sub-interface for sending messages and managing
	// the operator LLM conversation.
	Operator() OperatorService

	// Definitions returns the sub-interface for managing skills, agents, and
	// teams (CRUD, generation, promotion, coordinator detection).
	Definitions() DefinitionService

	// Jobs returns the sub-interface for listing, inspecting, and cancelling jobs.
	Jobs() JobService

	// Sessions returns the sub-interface for listing and inspecting agent sessions.
	Sessions() SessionService

	// Events returns the sub-interface for subscribing to the unified event stream.
	Events() EventService

	// System returns the sub-interface for health checks, model listing, and
	// MCP server status.
	System() SystemService
}

// ---------------------------------------------------------------------------
// OperatorService
// ---------------------------------------------------------------------------

// OperatorService handles the operator LLM conversation and user interaction.
type OperatorService interface {
	// SendMessage sends a user message to the operator event loop and returns
	// a turnID that correlates subsequent operator.text and operator.done events
	// back to this message. The method returns immediately — the operator
	// processes the message asynchronously and pushes events via the event stream.
	//
	// The TUI should enter streaming state after calling SendMessage and exit
	// streaming state when it receives an operator.done event with the matching
	// turnID.
	SendMessage(ctx context.Context, message string) (turnID string, err error)

	// RespondToPrompt sends the user's answer to an active ask_user prompt.
	// requestID must match the RequestID from the OperatorPromptPayload that
	// triggered the prompt.
	//
	// NOT YET FUNCTIONAL: there is no ask_user tool defined in the operator,
	// so this method, EventTypeOperatorPrompt, and OperatorPromptPayload are
	// all reserved for a future bidirectional-prompt feature but are not
	// exercised today. Calling this method just routes through to the
	// operator event loop with no recipient.
	RespondToPrompt(ctx context.Context, requestID string, response string) error

	// Status returns the current state of the operator (idle, streaming,
	// processing) and the model it is using. Used by clients to rebuild state
	// on reconnect (B4 concern).
	Status(ctx context.Context) (OperatorStatus, error)

	// History returns the conversation history for the current session.
	// Used by remote clients to hydrate the chat view on reconnect.
	// Returns entries in chronological order (oldest first).
	// The history is bounded to the most recent maxHistoryEntries entries.
	History(ctx context.Context) ([]ChatEntry, error)

	// RespondToBlocker submits the user's answers to a blocker reported by an agent.
	// The answers are formatted and sent to the operator as a user response event.
	// jobID and taskID identify the blocked task; answers correspond to the blocker's
	// Questions in order.
	RespondToBlocker(ctx context.Context, jobID, taskID string, answers []string) error
}

// ---------------------------------------------------------------------------
// DefinitionService
// ---------------------------------------------------------------------------

// DefinitionService manages skills, agents, and teams. It covers all CRUD
// operations, LLM-powered generation, team promotion, and coordinator detection.
//
// Generation and promotion operations are async: they return an operationID
// immediately and push operation.completed or operation.failed events via the
// event stream when done. The TUI should show a "generating…" indicator and
// update its state when the event arrives.
type DefinitionService interface {
	// --- Skills ---

	// ListSkills returns all skills known to the service, ordered by source
	// (user first, then system) and then by name.
	ListSkills(ctx context.Context) ([]Skill, error)

	// GetSkill returns a single skill by ID. Returns an error wrapping
	// ErrNotFound if the skill does not exist.
	GetSkill(ctx context.Context, id string) (Skill, error)

	// CreateSkill creates a new skill with the given name. It writes a template
	// .md file to the user skills directory and triggers a definition reload.
	// Returns the created skill.
	CreateSkill(ctx context.Context, name string) (Skill, error)

	// DeleteSkill deletes the skill with the given ID by removing its source
	// file. System skills (Source == "system") cannot be deleted and return an
	// error. Triggers a definition reload.
	DeleteSkill(ctx context.Context, id string) error

	// GenerateSkill asks the LLM to generate a skill definition for the given
	// prompt. Returns an operationID immediately. When the generation completes,
	// an operation.completed event is pushed with Kind == "generate_skill" and
	// the generated content in Result.Content. The implementation writes the
	// file and triggers a reload before pushing the event.
	//
	// On failure, an operation.failed event is pushed with the error message.
	GenerateSkill(ctx context.Context, prompt string) (operationID string, err error)

	// --- Agents ---

	// ListAgents returns all agents known to the service. The ordering is:
	// shared (non-team) agents alphabetically, then team-local agents by
	// "team/agent", then system agents alphabetically.
	ListAgents(ctx context.Context) ([]Agent, error)

	// GetAgent returns a single agent by ID. Returns an error wrapping
	// ErrNotFound if the agent does not exist.
	GetAgent(ctx context.Context, id string) (Agent, error)

	// CreateAgent creates a new shared agent with the given name. It writes a
	// template .md file to the user agents directory and triggers a definition
	// reload. Returns the created agent.
	CreateAgent(ctx context.Context, name string) (Agent, error)

	// DeleteAgent deletes the agent with the given ID by removing its source
	// file. Only user-owned shared agents (Source == "user", TeamID == "") can
	// be deleted; system and team-local agents return an error. Triggers a
	// definition reload.
	DeleteAgent(ctx context.Context, id string) error

	// AddSkillToAgent appends the named skill to the agent's .md file.
	// Returns an error if the agent or skill does not exist, if the agent is
	// read-only (system), or if the agent has no source path. Triggers a
	// definition reload.
	AddSkillToAgent(ctx context.Context, agentID string, skillName string) error

	// GenerateAgent asks the LLM to generate an agent definition for the given
	// prompt. Returns an operationID immediately. When the generation completes,
	// an operation.completed event is pushed with Kind == "generate_agent" and
	// the generated content in Result.Content. The implementation writes the
	// file and triggers a reload before pushing the event.
	//
	// On failure, an operation.failed event is pushed with the error message.
	GenerateAgent(ctx context.Context, prompt string) (operationID string, err error)

	// --- Teams ---

	// ListTeams returns all non-system teams as TeamView values (team + resolved
	// coordinator + workers). System teams (Source == "system") are excluded.
	ListTeams(ctx context.Context) ([]TeamView, error)

	// GetTeam returns a single team as a TeamView by team ID. Returns an error
	// wrapping ErrNotFound if the team does not exist.
	GetTeam(ctx context.Context, id string) (TeamView, error)

	// CreateTeam creates a new team directory with the given name under the user
	// teams directory. Returns the created TeamView. Triggers a definition reload.
	CreateTeam(ctx context.Context, name string) (TeamView, error)

	// DeleteTeam deletes the team directory for the given team ID. Read-only
	// teams (auto-detected from well-known directories) and system teams cannot
	// be deleted and return an error. If the team is an auto-team, a dismiss
	// marker is written so bootstrap does not re-create it on next startup.
	// Triggers a definition reload.
	DeleteTeam(ctx context.Context, id string) error

	// AddAgentToTeam adds the given agent to the team by appending the agent
	// name to the team's team.md agents list and copying the agent's source
	// .md file into the team's agents/ directory. Returns an error if the team
	// is read-only or if the agent has no source path. Triggers a definition
	// reload.
	AddAgentToTeam(ctx context.Context, teamID string, agentID string) error

	// SetCoordinator updates the team so that the agent with the given name is
	// the coordinator. It rewrites team.md's lead: field and updates mode: in
	// all agent files in the team's agents/ directory. Returns an error if the
	// team is read-only or if the agent is not found in the team. Triggers a
	// definition reload.
	SetCoordinator(ctx context.Context, teamID string, agentName string) error

	// PromoteTeam promotes an auto-detected team to a fully managed team.
	// Returns an operationID immediately. When the promotion completes, an
	// operation.completed event is pushed with Kind == "promote_team" and
	// Result.Content set to the team name. Triggers a definition reload.
	//
	// Promotion logic branches on team type:
	//   - Read-only auto-teams (e.g. ~/.claude/agents): copies agent files into
	//     a new managed team directory under ~/.config/toasters/user/teams/.
	//   - Bootstrap auto-teams (in user/teams/ with .auto-team marker): replaces
	//     the agents/ symlink with a real directory and writes team.md.
	//
	// On failure, an operation.failed event is pushed with the error message.
	PromoteTeam(ctx context.Context, teamID string) (operationID string, err error)

	// GenerateTeam asks the LLM to generate a team definition for the given
	// prompt, selecting agents from the available pool. Returns an operationID
	// immediately. When the generation completes, an operation.completed event
	// is pushed with Kind == "generate_team", Result.Content set to the team.md
	// content, and Result.AgentNames set to the agent names to assign.
	// The implementation writes the team directory and triggers a reload.
	//
	// On failure, an operation.failed event is pushed with the error message.
	GenerateTeam(ctx context.Context, prompt string) (operationID string, err error)

	// DetectCoordinator asks the LLM to pick the best coordinator for the team
	// from its current worker agents. Returns an operationID immediately. When
	// detection completes, an operation.completed event is pushed with
	// Kind == "detect_coordinator" and Result.Content set to the detected agent
	// name (empty if no match). The implementation calls SetCoordinator if a
	// match is found.
	//
	// On failure, an operation.failed event is pushed with the error message.
	DetectCoordinator(ctx context.Context, teamID string) (operationID string, err error)
}

// ---------------------------------------------------------------------------
// JobService
// ---------------------------------------------------------------------------

// JobService provides read-only access to jobs plus the ability to cancel them.
// Job creation is handled by the operator via its tool calls (create_job,
// create_task, assign_task) — not directly by the TUI.
type JobService interface {
	// List returns jobs matching the given filter. If filter is nil, all jobs
	// are returned. Results are ordered by creation time, newest first.
	// Pagination is supported via filter.Limit and filter.Offset.
	List(ctx context.Context, filter *JobListFilter) ([]Job, error)

	// ListAll returns all jobs regardless of status, ordered by creation time
	// newest first. Equivalent to List with an empty filter but no limit.
	// Used by the jobs modal which shows all historical jobs.
	ListAll(ctx context.Context) ([]Job, error)

	// Get returns a JobDetail for the given job ID, including its tasks and
	// recent progress reports. Returns an error wrapping ErrNotFound if the
	// job does not exist.
	Get(ctx context.Context, id string) (JobDetail, error)

	// Cancel cancels the job with the given ID by setting its status to
	// Cancelled. Only jobs in Active, Pending, SettingUp, or Decomposing
	// status can be cancelled; others return an error. The cancellation is
	// persisted to the DB immediately.
	Cancel(ctx context.Context, id string) error
}

// ---------------------------------------------------------------------------
// SessionService
// ---------------------------------------------------------------------------

// SessionService provides read-only access to agent sessions plus the ability
// to cancel them. Session creation is handled by the runtime when the operator
// assigns tasks to teams.
type SessionService interface {
	// List returns all currently active agent sessions as snapshots. Snapshots
	// carry real-time token counts from the in-process runtime (not the DB
	// records, which are only written on session completion).
	List(ctx context.Context) ([]SessionSnapshot, error)

	// Get returns a full SessionDetail for the given session ID, including the
	// accumulated output buffer, activity history, system prompt, and initial
	// message. Used by the output modal and for reconnect hydration (B4 concern:
	// clients call this on reconnect to rebuild the runtimeSlot state).
	// Returns an error wrapping ErrNotFound if the session does not exist.
	Get(ctx context.Context, id string) (SessionDetail, error)

	// Cancel cancels the session with the given ID. The session's goroutine
	// will be interrupted at the next tool-call boundary. Returns an error if
	// the session does not exist or is already complete.
	Cancel(ctx context.Context, id string) error
}

// ---------------------------------------------------------------------------
// SystemService
// ---------------------------------------------------------------------------

// SystemService provides health checks, model listing, and MCP server status.
type SystemService interface {
	// Health returns the current health status of the service, including
	// version and uptime. Always succeeds for LocalService; RemoteClient
	// returns an error if the server is unreachable.
	Health(ctx context.Context) (HealthStatus, error)

	// ListModels returns all models available from the configured LLM providers.
	// Used by the TUI to populate the model selector and display the active
	// model in the sidebar. Returns an error if the provider is unreachable.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// ListMCPServers returns the connection status and tool inventory for all
	// configured MCP servers. Includes both connected and failed servers.
	ListMCPServers(ctx context.Context) ([]MCPServerStatus, error)

	// GetProgressState returns the current full progress state snapshot.
	// Used by the HTTP server for the GET /api/v1/progress endpoint,
	// enabling clients to hydrate their state on connect/reconnect.
	// For real-time updates, clients should subscribe to the SSE event stream.
	GetProgressState(ctx context.Context) (ProgressState, error)

	// GetLogs returns the contents of the application log file.
	// Returns ("", nil) if the log file does not exist.
	GetLogs(ctx context.Context) (string, error)

	// ListCatalogProviders returns the full provider/model catalog from models.dev.
	// The data is cached in memory and refreshed periodically. Returns nil (not an
	// error) if the catalog is unavailable (e.g. no network).
	ListCatalogProviders(ctx context.Context) ([]CatalogProvider, error)
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrNotFound is returned by Get methods when the requested entity does not
// exist. Callers can check with errors.Is(err, service.ErrNotFound).
//
// Note: this is defined as a variable (not a type) so it can be used with
// errors.Is across package boundaries, including when wrapped with fmt.Errorf.
var ErrNotFound = errNotFound("not found")

type errNotFound string

func (e errNotFound) Error() string { return string(e) }
