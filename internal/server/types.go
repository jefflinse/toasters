package server

import (
	"encoding/json"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// ---------------------------------------------------------------------------
// Error response types
// ---------------------------------------------------------------------------

// ErrorResponse is the standard error envelope for all error responses.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries the error code and human-readable message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Pagination types
// ---------------------------------------------------------------------------

// PaginatedResponse wraps any list response with pagination metadata.
type PaginatedResponse[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// PaginationParams holds parsed pagination query parameters.
type PaginationParams struct {
	Limit  int
	Offset int
}

// ---------------------------------------------------------------------------
// Async operation response
// ---------------------------------------------------------------------------

// AsyncResponse is returned by all 202 Accepted endpoints.
type AsyncResponse struct {
	OperationID string `json:"operation_id"`
}

// TurnResponse is returned by POST /api/v1/operator/messages.
type TurnResponse struct {
	TurnID string `json:"turn_id"`
}

// ---------------------------------------------------------------------------
// Request body types
// ---------------------------------------------------------------------------

// SendMessageRequest is the body for POST /api/v1/operator/messages.
type SendMessageRequest struct {
	Message string `json:"message"`
}

// RespondToPromptRequest is the body for POST /api/v1/operator/prompts/{requestId}/respond.
type RespondToPromptRequest struct {
	Response string `json:"response"`
}

// RespondToBlockerRequest is the body for POST /api/v1/operator/blockers/{jobId}/{taskId}/respond.
type RespondToBlockerRequest struct {
	Answers []string `json:"answers"`
}

// CreateSkillRequest is the body for POST /api/v1/skills.
type CreateSkillRequest struct {
	Name string `json:"name"`
}

// GenerateRequest is the body for POST /api/v1/{skills,agents,teams}/generate.
type GenerateRequest struct {
	Prompt string `json:"prompt"`
}

// CreateAgentRequest is the body for POST /api/v1/agents.
type CreateAgentRequest struct {
	Name string `json:"name"`
}

// AddSkillToAgentRequest is the body for POST /api/v1/agents/{id}/skills.
type AddSkillToAgentRequest struct {
	SkillName string `json:"skill_name"`
}

// CreateTeamRequest is the body for POST /api/v1/teams.
type CreateTeamRequest struct {
	Name string `json:"name"`
}

// AddAgentToTeamRequest is the body for POST /api/v1/teams/{id}/agents.
type AddAgentToTeamRequest struct {
	AgentID string `json:"agent_id"`
}

// SetCoordinatorRequest is the body for PUT /api/v1/teams/{id}/coordinator.
type SetCoordinatorRequest struct {
	AgentName string `json:"agent_name"`
}

// ---------------------------------------------------------------------------
// SSE event wire type
// ---------------------------------------------------------------------------

// SSEEvent is the JSON envelope written to the SSE data field.
// It mirrors service.Event but with JSON-friendly field names.
type SSEEvent struct {
	Seq         uint64    `json:"seq"`
	Type        string    `json:"type"`
	Timestamp   time.Time `json:"timestamp"`
	TurnID      string    `json:"turn_id,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	OperationID string    `json:"operation_id,omitempty"`
	Payload     any       `json:"payload"`
}

// ---------------------------------------------------------------------------
// Health response
// ---------------------------------------------------------------------------

// HealthResponse is the body for GET /api/v1/health.
type HealthResponse struct {
	Status        string  `json:"status"`
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

// ---------------------------------------------------------------------------
// Logs response
// ---------------------------------------------------------------------------

// logsResponse is the body for GET /api/v1/logs.
type logsResponse struct {
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// Operator status response
// ---------------------------------------------------------------------------

// OperatorStatusResponse is the body for GET /api/v1/operator/status.
type OperatorStatusResponse struct {
	State         string `json:"state"`
	CurrentTurnID string `json:"current_turn_id"`
	ModelName     string `json:"model_name"`
}

// ---------------------------------------------------------------------------
// Wire types for service DTOs (snake_case JSON tags)
// ---------------------------------------------------------------------------

// wireJob is the JSON wire representation of a service.Job.
// WorkspaceDir is intentionally excluded — it is tagged json:"-" on the
// service DTO to prevent leaking filesystem paths to clients.
type wireJob struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

func jobToWire(j service.Job) wireJob {
	return wireJob{
		ID:          j.ID,
		Title:       j.Title,
		Description: j.Description,
		Type:        j.Type,
		Status:      string(j.Status),
		CreatedAt:   j.CreatedAt,
		UpdatedAt:   j.UpdatedAt,
		Metadata:    j.Metadata,
	}
}

// wireTask is the JSON wire representation of a service.Task.
type wireTask struct {
	ID              string          `json:"id"`
	JobID           string          `json:"job_id"`
	Title           string          `json:"title"`
	Status          string          `json:"status"`
	AgentID         string          `json:"agent_id,omitempty"`
	TeamID          string          `json:"team_id,omitempty"`
	ParentID        string          `json:"parent_id,omitempty"`
	SortOrder       int             `json:"sort_order"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Summary         string          `json:"summary,omitempty"`
	ResultSummary   string          `json:"result_summary,omitempty"`
	Recommendations string          `json:"recommendations,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

func taskToWire(t service.Task) wireTask {
	return wireTask{
		ID:              t.ID,
		JobID:           t.JobID,
		Title:           t.Title,
		Status:          string(t.Status),
		AgentID:         t.AgentID,
		TeamID:          t.TeamID,
		ParentID:        t.ParentID,
		SortOrder:       t.SortOrder,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
		Summary:         t.Summary,
		ResultSummary:   t.ResultSummary,
		Recommendations: t.Recommendations,
		Metadata:        t.Metadata,
	}
}

// wireProgressReport is the JSON wire representation of a service.ProgressReport.
type wireProgressReport struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	TaskID    string    `json:"task_id,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func progressReportToWire(p service.ProgressReport) wireProgressReport {
	return wireProgressReport{
		ID:        p.ID,
		JobID:     p.JobID,
		TaskID:    p.TaskID,
		AgentID:   p.AgentID,
		Status:    p.Status,
		Message:   p.Message,
		CreatedAt: p.CreatedAt,
	}
}

// wireJobDetail is the JSON wire representation of a service.JobDetail.
type wireJobDetail struct {
	Job      wireJob              `json:"job"`
	Tasks    []wireTask           `json:"tasks"`
	Progress []wireProgressReport `json:"progress"`
}

func jobDetailToWire(jd service.JobDetail) wireJobDetail {
	tasks := make([]wireTask, 0, len(jd.Tasks))
	for _, t := range jd.Tasks {
		tasks = append(tasks, taskToWire(t))
	}
	progress := make([]wireProgressReport, 0, len(jd.Progress))
	for _, p := range jd.Progress {
		progress = append(progress, progressReportToWire(p))
	}
	return wireJobDetail{
		Job:      jobToWire(jd.Job),
		Tasks:    tasks,
		Progress: progress,
	}
}

// wireSkill is the JSON wire representation of a service.Skill.
type wireSkill struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Tools       []string  `json:"tools"`
	Prompt      string    `json:"prompt,omitempty"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func skillToWire(s service.Skill) wireSkill {
	tools := s.Tools
	if tools == nil {
		tools = []string{}
	}
	return wireSkill{
		ID:          s.ID,
		Name:        s.Name,
		Description: s.Description,
		Tools:       tools,
		Prompt:      s.Prompt,
		Source:      s.Source,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
	}
}

// wireAgent is the JSON wire representation of a service.Agent.
type wireAgent struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	Mode            string    `json:"mode,omitempty"`
	Model           string    `json:"model,omitempty"`
	Provider        string    `json:"provider,omitempty"`
	Temperature     *float64  `json:"temperature,omitempty"`
	SystemPrompt    string    `json:"system_prompt,omitempty"`
	Tools           []string  `json:"tools"`
	DisallowedTools []string  `json:"disallowed_tools"`
	Skills          []string  `json:"skills"`
	PermissionMode  string    `json:"permission_mode,omitempty"`
	MaxTurns        *int      `json:"max_turns,omitempty"`
	Color           string    `json:"color,omitempty"`
	Hidden          bool      `json:"hidden"`
	Disabled        bool      `json:"disabled"`
	Source          string    `json:"source"`
	TeamID          string    `json:"team_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func agentToWire(a service.Agent) wireAgent {
	tools := a.Tools
	if tools == nil {
		tools = []string{}
	}
	disallowed := a.DisallowedTools
	if disallowed == nil {
		disallowed = []string{}
	}
	skills := a.Skills
	if skills == nil {
		skills = []string{}
	}
	return wireAgent{
		ID:              a.ID,
		Name:            a.Name,
		Description:     a.Description,
		Mode:            a.Mode,
		Model:           a.Model,
		Provider:        a.Provider,
		Temperature:     a.Temperature,
		SystemPrompt:    a.SystemPrompt,
		Tools:           tools,
		DisallowedTools: disallowed,
		Skills:          skills,
		PermissionMode:  a.PermissionMode,
		MaxTurns:        a.MaxTurns,
		Color:           a.Color,
		Hidden:          a.Hidden,
		Disabled:        a.Disabled,
		Source:          a.Source,
		TeamID:          a.TeamID,
		CreatedAt:       a.CreatedAt,
		UpdatedAt:       a.UpdatedAt,
	}
}

// wireTeam is the JSON wire representation of a service.Team.
type wireTeam struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	LeadAgent   string    `json:"lead_agent,omitempty"`
	Skills      []string  `json:"skills"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	Culture     string    `json:"culture,omitempty"`
	Source      string    `json:"source"`
	IsAuto      bool      `json:"is_auto"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func teamToWire(t service.Team) wireTeam {
	skills := t.Skills
	if skills == nil {
		skills = []string{}
	}
	return wireTeam{
		ID:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		LeadAgent:   t.LeadAgent,
		Skills:      skills,
		Provider:    t.Provider,
		Model:       t.Model,
		Culture:     t.Culture,
		Source:      t.Source,
		IsAuto:      t.IsAuto,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

// wireTeamView is the JSON wire representation of a service.TeamView.
type wireTeamView struct {
	Team        wireTeam    `json:"team"`
	Coordinator *wireAgent  `json:"coordinator"`
	Workers     []wireAgent `json:"workers"`
	IsReadOnly  bool        `json:"is_readonly"`
	IsSystem    bool        `json:"is_system"`
}

func teamViewToWire(tv service.TeamView) wireTeamView {
	var coord *wireAgent
	if tv.Coordinator != nil {
		w := agentToWire(*tv.Coordinator)
		coord = &w
	}
	workers := make([]wireAgent, 0, len(tv.Workers))
	for _, w := range tv.Workers {
		workers = append(workers, agentToWire(w))
	}
	return wireTeamView{
		Team:        teamToWire(tv.Team),
		Coordinator: coord,
		Workers:     workers,
		IsReadOnly:  tv.IsReadOnly,
		IsSystem:    tv.IsSystem,
	}
}

// wireSessionSnapshot is the JSON wire representation of a service.SessionSnapshot.
type wireSessionSnapshot struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	TeamName  string    `json:"team_name,omitempty"`
	JobID     string    `json:"job_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Status    string    `json:"status"`
	Model     string    `json:"model,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	StartTime time.Time `json:"start_time"`
	TokensIn  int64     `json:"tokens_in"`
	TokensOut int64     `json:"tokens_out"`
}

func sessionSnapshotToWire(s service.SessionSnapshot) wireSessionSnapshot {
	return wireSessionSnapshot{
		ID:        s.ID,
		AgentID:   s.AgentID,
		TeamName:  s.TeamName,
		JobID:     s.JobID,
		TaskID:    s.TaskID,
		Status:    s.Status,
		Model:     s.Model,
		Provider:  s.Provider,
		StartTime: s.StartTime,
		TokensIn:  s.TokensIn,
		TokensOut: s.TokensOut,
	}
}

// wireActivityItem is the JSON wire representation of a service.ActivityItem.
type wireActivityItem struct {
	Label    string `json:"label"`
	ToolName string `json:"tool_name"`
}

// wireSessionDetail is the JSON wire representation of a service.SessionDetail.
type wireSessionDetail struct {
	Snapshot       wireSessionSnapshot `json:"snapshot"`
	SystemPrompt   string              `json:"system_prompt,omitempty"`
	InitialMessage string              `json:"initial_message,omitempty"`
	Output         string              `json:"output,omitempty"`
	Activities     []wireActivityItem  `json:"activities"`
	AgentName      string              `json:"agent_name"`
	TeamName       string              `json:"team_name,omitempty"`
	Task           string              `json:"task,omitempty"`
}

func sessionDetailToWire(sd service.SessionDetail) wireSessionDetail {
	activities := make([]wireActivityItem, 0, len(sd.Activities))
	for _, a := range sd.Activities {
		activities = append(activities, wireActivityItem{
			Label:    a.Label,
			ToolName: a.ToolName,
		})
	}
	return wireSessionDetail{
		Snapshot:       sessionSnapshotToWire(sd.Snapshot),
		SystemPrompt:   sd.SystemPrompt,
		InitialMessage: sd.InitialMessage,
		Output:         sd.Output,
		Activities:     activities,
		AgentName:      sd.AgentName,
		TeamName:       sd.TeamName,
		Task:           sd.Task,
	}
}

// wireModelInfo is the JSON wire representation of a service.ModelInfo.
type wireModelInfo struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Provider            string `json:"provider"`
	State               string `json:"state"`
	MaxContextLength    int    `json:"max_context_length"`
	LoadedContextLength int    `json:"loaded_context_length"`
}

func modelInfoToWire(m service.ModelInfo) wireModelInfo {
	return wireModelInfo{
		ID:                  m.ID,
		Name:                m.Name,
		Provider:            m.Provider,
		State:               m.State,
		MaxContextLength:    m.MaxContextLength,
		LoadedContextLength: m.LoadedContextLength,
	}
}

// wireMCPToolInfo is the JSON wire representation of a service.MCPToolInfo.
type wireMCPToolInfo struct {
	NamespacedName string          `json:"namespaced_name"`
	OriginalName   string          `json:"original_name"`
	ServerName     string          `json:"server_name"`
	Description    string          `json:"description"`
	InputSchema    json.RawMessage `json:"input_schema"`
}

// wireMCPServerStatus is the JSON wire representation of a service.MCPServerStatus.
type wireMCPServerStatus struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	State     string            `json:"state"`
	Error     string            `json:"error,omitempty"`
	ToolCount int               `json:"tool_count"`
	Tools     []wireMCPToolInfo `json:"tools"`
}

func mcpServerStatusToWire(s service.MCPServerStatus) wireMCPServerStatus {
	tools := make([]wireMCPToolInfo, 0, len(s.Tools))
	for _, t := range s.Tools {
		tools = append(tools, wireMCPToolInfo{
			NamespacedName: t.NamespacedName,
			OriginalName:   t.OriginalName,
			ServerName:     t.ServerName,
			Description:    t.Description,
			InputSchema:    t.InputSchema,
		})
	}
	return wireMCPServerStatus{
		Name:      s.Name,
		Transport: s.Transport,
		State:     string(s.State),
		Error:     s.Error,
		ToolCount: s.ToolCount,
		Tools:     tools,
	}
}

// wireChatMessage is the JSON wire representation of a service.ChatMessage.
type wireChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// wireToolCall is the JSON wire representation of a service.ToolCall.
type wireToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// wireChatEntry is the JSON wire representation of a service.ChatEntry.
type wireChatEntry struct {
	Message    wireChatMessage `json:"message"`
	Timestamp  time.Time       `json:"timestamp"`
	Reasoning  string          `json:"reasoning,omitempty"`
	ClaudeMeta string          `json:"claude_meta,omitempty"`
}

func chatEntryToWire(e service.ChatEntry) wireChatEntry {
	toolCalls := make([]wireToolCall, 0, len(e.Message.ToolCalls))
	for _, tc := range e.Message.ToolCalls {
		toolCalls = append(toolCalls, wireToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}
	return wireChatEntry{
		Message: wireChatMessage{
			Role:       string(e.Message.Role),
			Content:    e.Message.Content,
			ToolCalls:  toolCalls,
			ToolCallID: e.Message.ToolCallID,
		},
		Timestamp:  e.Timestamp,
		Reasoning:  e.Reasoning,
		ClaudeMeta: e.ClaudeMeta,
	}
}

// wireProgressState is the JSON wire representation of a service.ProgressState.
type wireProgressState struct {
	Jobs           []wireJob                       `json:"jobs"`
	Tasks          map[string][]wireTask           `json:"tasks"`
	Reports        map[string][]wireProgressReport `json:"reports"`
	ActiveSessions []wireAgentSession              `json:"active_sessions"`
	LiveSnapshots  []wireSessionSnapshot           `json:"live_snapshots"`
	FeedEntries    []wireFeedEntry                 `json:"feed_entries"`
}

// wireAgentSession is the JSON wire representation of a service.AgentSession.
type wireAgentSession struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agent_id"`
	JobID     string     `json:"job_id,omitempty"`
	TaskID    string     `json:"task_id,omitempty"`
	Status    string     `json:"status"`
	Model     string     `json:"model,omitempty"`
	Provider  string     `json:"provider,omitempty"`
	TokensIn  int64      `json:"tokens_in"`
	TokensOut int64      `json:"tokens_out"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	CostUSD   *float64   `json:"cost_usd,omitempty"`
}

func agentSessionToWire(s service.AgentSession) wireAgentSession {
	return wireAgentSession{
		ID:        s.ID,
		AgentID:   s.AgentID,
		JobID:     s.JobID,
		TaskID:    s.TaskID,
		Status:    string(s.Status),
		Model:     s.Model,
		Provider:  s.Provider,
		TokensIn:  s.TokensIn,
		TokensOut: s.TokensOut,
		StartedAt: s.StartedAt,
		EndedAt:   s.EndedAt,
		CostUSD:   s.CostUSD,
	}
}

// wireFeedEntry is the JSON wire representation of a service.FeedEntry.
type wireFeedEntry struct {
	ID        int64           `json:"id"`
	JobID     string          `json:"job_id,omitempty"`
	EntryType string          `json:"entry_type"`
	Content   string          `json:"content"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func feedEntryToWire(fe service.FeedEntry) wireFeedEntry {
	return wireFeedEntry{
		ID:        fe.ID,
		JobID:     fe.JobID,
		EntryType: string(fe.EntryType),
		Content:   fe.Content,
		Metadata:  fe.Metadata,
		CreatedAt: fe.CreatedAt,
	}
}

func progressStateToWire(ps service.ProgressState) wireProgressState {
	jobs := make([]wireJob, 0, len(ps.Jobs))
	for _, j := range ps.Jobs {
		jobs = append(jobs, jobToWire(j))
	}

	tasks := make(map[string][]wireTask, len(ps.Tasks))
	for k, v := range ps.Tasks {
		wt := make([]wireTask, 0, len(v))
		for _, t := range v {
			wt = append(wt, taskToWire(t))
		}
		tasks[k] = wt
	}

	reports := make(map[string][]wireProgressReport, len(ps.Reports))
	for k, v := range ps.Reports {
		wr := make([]wireProgressReport, 0, len(v))
		for _, r := range v {
			wr = append(wr, progressReportToWire(r))
		}
		reports[k] = wr
	}

	activeSessions := make([]wireAgentSession, 0, len(ps.ActiveSessions))
	for _, s := range ps.ActiveSessions {
		activeSessions = append(activeSessions, agentSessionToWire(s))
	}

	liveSnapshots := make([]wireSessionSnapshot, 0, len(ps.LiveSnapshots))
	for _, s := range ps.LiveSnapshots {
		liveSnapshots = append(liveSnapshots, sessionSnapshotToWire(s))
	}

	feedEntries := make([]wireFeedEntry, 0, len(ps.FeedEntries))
	for _, fe := range ps.FeedEntries {
		feedEntries = append(feedEntries, feedEntryToWire(fe))
	}

	return wireProgressState{
		Jobs:           jobs,
		Tasks:          tasks,
		Reports:        reports,
		ActiveSessions: activeSessions,
		LiveSnapshots:  liveSnapshots,
		FeedEntries:    feedEntries,
	}
}

// ---------------------------------------------------------------------------
// SSE payload wire types (snake_case JSON tags for event payloads)
// ---------------------------------------------------------------------------

type wireOperatorTextPayload struct {
	Text      string `json:"text"`
	Reasoning string `json:"reasoning,omitempty"`
}

type wireOperatorDonePayload struct {
	ModelName       string `json:"model_name"`
	TokensIn        int    `json:"tokens_in"`
	TokensOut       int    `json:"tokens_out"`
	ReasoningTokens int    `json:"reasoning_tokens"`
}

type wireOperatorPromptPayload struct {
	RequestID       string        `json:"request_id"`
	Question        string        `json:"question"`
	Options         []string      `json:"options,omitempty"`
	ConfirmDispatch bool          `json:"confirm_dispatch"`
	PendingDispatch *wireToolCall `json:"pending_dispatch,omitempty"`
}

type wireTaskAssignedPayload struct {
	TaskID string `json:"task_id"`
	JobID  string `json:"job_id"`
	TeamID string `json:"team_id"`
	Title  string `json:"title"`
}

type wireTaskStartedPayload struct {
	TaskID string `json:"task_id"`
	JobID  string `json:"job_id"`
	TeamID string `json:"team_id"`
	Title  string `json:"title"`
}

type wireTaskCompletedPayload struct {
	TaskID          string `json:"task_id"`
	JobID           string `json:"job_id"`
	TeamID          string `json:"team_id"`
	Summary         string `json:"summary"`
	Recommendations string `json:"recommendations,omitempty"`
	HasNextTask     bool   `json:"has_next_task"`
}

type wireTaskFailedPayload struct {
	TaskID string `json:"task_id"`
	JobID  string `json:"job_id"`
	TeamID string `json:"team_id"`
	Error  string `json:"error"`
}

type wireBlockerReportedPayload struct {
	TaskID      string   `json:"task_id"`
	TeamID      string   `json:"team_id"`
	AgentID     string   `json:"agent_id"`
	Description string   `json:"description"`
	Questions   []string `json:"questions,omitempty"`
}

type wireJobCompletedPayload struct {
	JobID   string `json:"job_id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type wireProgressUpdatePayload struct {
	State wireProgressState `json:"state"`
}

type wireSessionStartedPayload struct {
	SessionID      string `json:"session_id"`
	AgentName      string `json:"agent_name"`
	TeamName       string `json:"team_name,omitempty"`
	Task           string `json:"task,omitempty"`
	JobID          string `json:"job_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	SystemPrompt   string `json:"system_prompt,omitempty"`
	InitialMessage string `json:"initial_message,omitempty"`
}

type wireSessionTextPayload struct {
	Text string `json:"text"`
}

type wireSessionToolCallPayload struct {
	ToolCall wireToolCall `json:"tool_call"`
}

type wireSessionToolResultPayload struct {
	Result wireToolCallResult `json:"result"`
}

type wireToolCallResult struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

type wireSessionDonePayload struct {
	AgentName string `json:"agent_name"`
	JobID     string `json:"job_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	Status    string `json:"status"`
	FinalText string `json:"final_text,omitempty"`
}

type wireOperationCompletedPayload struct {
	Kind   string              `json:"kind"`
	Result wireOperationResult `json:"result"`
}

type wireOperationResult struct {
	OperationID string   `json:"operation_id"`
	Content     string   `json:"content,omitempty"`
	AgentNames  []string `json:"agent_names,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type wireOperationFailedPayload struct {
	Kind  string `json:"kind"`
	Error string `json:"error"`
}

type wireHeartbeatPayload struct {
	ServerTime time.Time `json:"server_time"`
}

type wireConnectionLostPayload struct {
	Error string `json:"error"`
}

type wireConnectionRestoredPayload struct{}

// eventPayloadToWire converts a service event payload to its wire representation.
func eventPayloadToWire(ev service.Event) any {
	switch p := ev.Payload.(type) {
	case service.OperatorTextPayload:
		return wireOperatorTextPayload{Text: p.Text, Reasoning: p.Reasoning}
	case service.OperatorDonePayload:
		return wireOperatorDonePayload{
			ModelName: p.ModelName, TokensIn: p.TokensIn,
			TokensOut: p.TokensOut, ReasoningTokens: p.ReasoningTokens,
		}
	case service.OperatorPromptPayload:
		w := wireOperatorPromptPayload{
			RequestID:       p.RequestID,
			Question:        p.Question,
			Options:         p.Options,
			ConfirmDispatch: p.ConfirmDispatch,
		}
		if p.PendingDispatch != nil {
			w.PendingDispatch = &wireToolCall{
				ID: p.PendingDispatch.ID, Name: p.PendingDispatch.Name,
				Arguments: p.PendingDispatch.Arguments,
			}
		}
		return w
	case service.TaskAssignedPayload:
		return wireTaskAssignedPayload{TaskID: p.TaskID, JobID: p.JobID, TeamID: p.TeamID, Title: p.Title}
	case service.TaskStartedPayload:
		return wireTaskStartedPayload{TaskID: p.TaskID, JobID: p.JobID, TeamID: p.TeamID, Title: p.Title}
	case service.TaskCompletedPayload:
		return wireTaskCompletedPayload{
			TaskID: p.TaskID, JobID: p.JobID, TeamID: p.TeamID,
			Summary: p.Summary, Recommendations: p.Recommendations, HasNextTask: p.HasNextTask,
		}
	case service.TaskFailedPayload:
		return wireTaskFailedPayload{TaskID: p.TaskID, JobID: p.JobID, TeamID: p.TeamID, Error: p.Error}
	case service.BlockerReportedPayload:
		return wireBlockerReportedPayload{
			TaskID: p.TaskID, TeamID: p.TeamID, AgentID: p.AgentID,
			Description: p.Description, Questions: p.Questions,
		}
	case service.JobCompletedPayload:
		return wireJobCompletedPayload{JobID: p.JobID, Title: p.Title, Summary: p.Summary}
	case service.ProgressUpdatePayload:
		return wireProgressUpdatePayload{State: progressStateToWire(p.State)}
	case service.SessionStartedPayload:
		return wireSessionStartedPayload{
			SessionID: p.SessionID, AgentName: p.AgentName, TeamName: p.TeamName,
			Task: p.Task, JobID: p.JobID, TaskID: p.TaskID,
			SystemPrompt: p.SystemPrompt, InitialMessage: p.InitialMessage,
		}
	case service.SessionTextPayload:
		return wireSessionTextPayload{Text: p.Text}
	case service.SessionToolCallPayload:
		return wireSessionToolCallPayload{
			ToolCall: wireToolCall{
				ID: p.ToolCall.ID, Name: p.ToolCall.Name, Arguments: p.ToolCall.Arguments,
			},
		}
	case service.SessionToolResultPayload:
		return wireSessionToolResultPayload{
			Result: wireToolCallResult{
				CallID: p.Result.CallID, Name: p.Result.Name,
				Result: p.Result.Result, Error: p.Result.Error,
			},
		}
	case service.SessionDonePayload:
		return wireSessionDonePayload{
			AgentName: p.AgentName, JobID: p.JobID, TaskID: p.TaskID,
			Status: p.Status, FinalText: p.FinalText,
		}
	case service.OperationCompletedPayload:
		return wireOperationCompletedPayload{
			Kind: p.Kind,
			Result: wireOperationResult{
				OperationID: p.Result.OperationID, Content: p.Result.Content,
				AgentNames: p.Result.AgentNames, Error: p.Result.Error,
			},
		}
	case service.OperationFailedPayload:
		return wireOperationFailedPayload{Kind: p.Kind, Error: p.Error}
	case service.HeartbeatPayload:
		return wireHeartbeatPayload{ServerTime: p.ServerTime}
	case service.ConnectionLostPayload:
		return wireConnectionLostPayload{Error: p.Error}
	case service.ConnectionRestoredPayload:
		return wireConnectionRestoredPayload{}
	default:
		return ev.Payload
	}
}
