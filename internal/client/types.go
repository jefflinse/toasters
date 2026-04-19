// Package client provides a RemoteClient that implements service.Service over
// HTTP+SSE. It connects to a standalone Toasters server and translates REST
// responses and SSE events into service-level types.
//
// This package defines its own wire types (JSON-tagged structs) that mirror the
// server's wire format. It does NOT import internal/server — the client and
// server are independently deployable.
package client

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// ---------------------------------------------------------------------------
// Entity wire types (match server's snake_case JSON exactly)
// ---------------------------------------------------------------------------

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

type wireTask struct {
	ID              string          `json:"id"`
	JobID           string          `json:"job_id"`
	Title           string          `json:"title"`
	Status          string          `json:"status"`
	WorkerID        string          `json:"worker_id,omitempty"`
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

type wireProgressReport struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	TaskID    string    `json:"task_id,omitempty"`
	WorkerID  string    `json:"worker_id,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type wireJobDetail struct {
	Job      wireJob              `json:"job"`
	Tasks    []wireTask           `json:"tasks"`
	Progress []wireProgressReport `json:"progress"`
}

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

type wireWorker struct {
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

type wireTeam struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	LeadWorker  string    `json:"lead_worker,omitempty"`
	Skills      []string  `json:"skills"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	Culture     string    `json:"culture,omitempty"`
	Source      string    `json:"source"`
	IsAuto      bool      `json:"is_auto"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type wireTeamView struct {
	Team        wireTeam     `json:"team"`
	Coordinator *wireWorker  `json:"coordinator"`
	Workers     []wireWorker `json:"workers"`
	IsReadOnly  bool         `json:"is_readonly"`
	IsSystem    bool         `json:"is_system"`
}

type wireSessionSnapshot struct {
	ID        string    `json:"id"`
	WorkerID  string    `json:"worker_id"`
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

type wireActivityItem struct {
	Label    string `json:"label"`
	ToolName string `json:"tool_name"`
}

type wireSessionDetail struct {
	Snapshot       wireSessionSnapshot `json:"snapshot"`
	SystemPrompt   string              `json:"system_prompt,omitempty"`
	InitialMessage string              `json:"initial_message,omitempty"`
	Output         string              `json:"output,omitempty"`
	Activities     []wireActivityItem  `json:"activities"`
	WorkerName     string              `json:"worker_name"`
	TeamName       string              `json:"team_name,omitempty"`
	Task           string              `json:"task,omitempty"`
}

type wireModelInfo struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Provider            string `json:"provider"`
	State               string `json:"state"`
	MaxContextLength    int    `json:"max_context_length"`
	LoadedContextLength int    `json:"loaded_context_length"`
}

type wireAddProviderRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Endpoint string `json:"endpoint,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

type wireCatalogProvider struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	API    string             `json:"api,omitempty"`
	Doc    string             `json:"doc,omitempty"`
	Env    []string           `json:"env,omitempty"`
	Models []wireCatalogModel `json:"models"`
}

type wireCatalogModel struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Family           string  `json:"family,omitempty"`
	ToolCall         bool    `json:"tool_call"`
	Reasoning        bool    `json:"reasoning"`
	StructuredOutput bool    `json:"structured_output"`
	OpenWeights      bool    `json:"open_weights"`
	ContextLimit     int     `json:"context_limit"`
	OutputLimit      int     `json:"output_limit"`
	InputCost        float64 `json:"input_cost"`
	OutputCost       float64 `json:"output_cost"`
}

type wireMCPToolInfo struct {
	NamespacedName string          `json:"namespaced_name"`
	OriginalName   string          `json:"original_name"`
	ServerName     string          `json:"server_name"`
	Description    string          `json:"description"`
	InputSchema    json.RawMessage `json:"input_schema"`
}

type wireMCPServerStatus struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	State     string            `json:"state"`
	Error     string            `json:"error,omitempty"`
	ToolCount int               `json:"tool_count"`
	Tools     []wireMCPToolInfo `json:"tools"`
}

type wireChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type wireToolCallResult struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

type wireChatEntry struct {
	Message    wireChatMessage `json:"message"`
	Timestamp  time.Time       `json:"timestamp"`
	Reasoning  string          `json:"reasoning,omitempty"`
	ClaudeMeta string          `json:"claude_meta,omitempty"`
}

type wireProgressState struct {
	Jobs           []wireJob                       `json:"jobs"`
	Tasks          map[string][]wireTask           `json:"tasks"`
	Reports        map[string][]wireProgressReport `json:"reports"`
	ActiveSessions []wireWorkerSession              `json:"active_sessions"`
	LiveSnapshots  []wireSessionSnapshot           `json:"live_snapshots"`
	FeedEntries    []wireFeedEntry                 `json:"feed_entries"`
}

type wireWorkerSession struct {
	ID        string     `json:"id"`
	WorkerID  string     `json:"worker_id"`
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

type wireFeedEntry struct {
	ID        int64           `json:"id"`
	JobID     string          `json:"job_id,omitempty"`
	EntryType string          `json:"entry_type"`
	Content   string          `json:"content"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Response envelope types
// ---------------------------------------------------------------------------

type paginatedResponse[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type asyncResponse struct {
	OperationID string `json:"operation_id"`
}

type turnResponse struct {
	TurnID string `json:"turn_id"`
}

type healthResponse struct {
	Status        string  `json:"status"`
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

type operatorStatusResponse struct {
	State         string `json:"state"`
	CurrentTurnID string `json:"current_turn_id"`
	ModelName     string `json:"model_name"`
	Endpoint      string `json:"endpoint"`
}

type logsResponse struct {
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// SSE event envelope
// ---------------------------------------------------------------------------

// sseEvent is the JSON envelope for SSE data lines. Payload is kept as
// json.RawMessage for two-pass deserialization: first decode the envelope,
// then switch on Type to unmarshal the payload into the correct wire type.
type sseEvent struct {
	Seq         uint64          `json:"seq"`
	Type        string          `json:"type"`
	Timestamp   time.Time       `json:"timestamp"`
	TurnID      string          `json:"turn_id,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	OperationID string          `json:"operation_id,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

// ---------------------------------------------------------------------------
// SSE payload wire types (19 types matching server's wire*Payload types)
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
	Source          string        `json:"source,omitempty"`
	ConfirmDispatch bool          `json:"confirm_dispatch"`
	PendingDispatch *wireToolCall `json:"pending_dispatch,omitempty"`
}

type wireJobCreatedPayload struct {
	JobID       string `json:"job_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type wireTaskCreatedPayload struct {
	TaskID string `json:"task_id"`
	JobID  string `json:"job_id"`
	Title  string `json:"title"`
	TeamID string `json:"team_id,omitempty"`
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
	WorkerID    string   `json:"worker_id"`
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
	WorkerName     string `json:"worker_name"`
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

type wireSessionDonePayload struct {
	WorkerName string `json:"worker_name"`
	JobID      string `json:"job_id,omitempty"`
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

type wireGraphNodeStartedPayload struct {
	JobID  string `json:"job_id"`
	TaskID string `json:"task_id"`
	Node   string `json:"node"`
}

type wireGraphNodeCompletedPayload struct {
	JobID  string `json:"job_id"`
	TaskID string `json:"task_id"`
	Node   string `json:"node"`
	Status string `json:"status"`
}

type wireGraphCompletedPayload struct {
	JobID   string `json:"job_id"`
	TaskID  string `json:"task_id"`
	Summary string `json:"summary"`
}

type wireGraphFailedPayload struct {
	JobID  string `json:"job_id"`
	TaskID string `json:"task_id"`
	Error  string `json:"error"`
}

// ---------------------------------------------------------------------------
// Wire → service converter functions
// ---------------------------------------------------------------------------

func wireJobToService(w wireJob) service.Job {
	return service.Job{
		ID:          w.ID,
		Title:       w.Title,
		Description: w.Description,
		Type:        w.Type,
		Status:      service.JobStatus(w.Status),
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
		Metadata:    w.Metadata,
	}
}

func wireTaskToService(w wireTask) service.Task {
	return service.Task{
		ID:              w.ID,
		JobID:           w.JobID,
		Title:           w.Title,
		Status:          service.TaskStatus(w.Status),
		WorkerID:        w.WorkerID,
		TeamID:          w.TeamID,
		ParentID:        w.ParentID,
		SortOrder:       w.SortOrder,
		CreatedAt:       w.CreatedAt,
		UpdatedAt:       w.UpdatedAt,
		Summary:         w.Summary,
		ResultSummary:   w.ResultSummary,
		Recommendations: w.Recommendations,
		Metadata:        w.Metadata,
	}
}

func wireProgressReportToService(w wireProgressReport) service.ProgressReport {
	return service.ProgressReport{
		ID:        w.ID,
		JobID:     w.JobID,
		TaskID:    w.TaskID,
		WorkerID:  w.WorkerID,
		Status:    w.Status,
		Message:   w.Message,
		CreatedAt: w.CreatedAt,
	}
}

func wireJobDetailToService(w wireJobDetail) service.JobDetail {
	tasks := make([]service.Task, 0, len(w.Tasks))
	for _, t := range w.Tasks {
		tasks = append(tasks, wireTaskToService(t))
	}
	progress := make([]service.ProgressReport, 0, len(w.Progress))
	for _, p := range w.Progress {
		progress = append(progress, wireProgressReportToService(p))
	}
	return service.JobDetail{
		Job:      wireJobToService(w.Job),
		Tasks:    tasks,
		Progress: progress,
	}
}

func wireSkillToService(w wireSkill) service.Skill {
	return service.Skill{
		ID:          w.ID,
		Name:        w.Name,
		Description: w.Description,
		Tools:       w.Tools,
		Prompt:      w.Prompt,
		Source:      w.Source,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	}
}

func wireWorkerToService(w wireWorker) service.Worker {
	return service.Worker{
		ID:              w.ID,
		Name:            w.Name,
		Description:     w.Description,
		Mode:            w.Mode,
		Model:           w.Model,
		Provider:        w.Provider,
		Temperature:     w.Temperature,
		SystemPrompt:    w.SystemPrompt,
		Tools:           w.Tools,
		DisallowedTools: w.DisallowedTools,
		Skills:          w.Skills,
		PermissionMode:  w.PermissionMode,
		MaxTurns:        w.MaxTurns,
		Color:           w.Color,
		Hidden:          w.Hidden,
		Disabled:        w.Disabled,
		Source:          w.Source,
		TeamID:          w.TeamID,
		CreatedAt:       w.CreatedAt,
		UpdatedAt:       w.UpdatedAt,
	}
}

func wireTeamToService(w wireTeam) service.Team {
	return service.Team{
		ID:          w.ID,
		Name:        w.Name,
		Description: w.Description,
		LeadWorker:   w.LeadWorker,
		Skills:      w.Skills,
		Provider:    w.Provider,
		Model:       w.Model,
		Culture:     w.Culture,
		Source:      w.Source,
		IsAuto:      w.IsAuto,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	}
}

func wireTeamViewToService(w wireTeamView) service.TeamView {
	tv := service.TeamView{
		Team:       wireTeamToService(w.Team),
		Workers:    make([]service.Worker, 0, len(w.Workers)),
		IsReadOnly: w.IsReadOnly,
		IsSystem:   w.IsSystem,
	}
	if w.Coordinator != nil {
		a := wireWorkerToService(*w.Coordinator)
		tv.Coordinator = &a
	}
	for _, worker := range w.Workers {
		tv.Workers = append(tv.Workers, wireWorkerToService(worker))
	}
	return tv
}

func wireSessionSnapshotToService(w wireSessionSnapshot) service.SessionSnapshot {
	return service.SessionSnapshot{
		ID:        w.ID,
		WorkerID:  w.WorkerID,
		TeamName:  w.TeamName,
		JobID:     w.JobID,
		TaskID:    w.TaskID,
		Status:    w.Status,
		Model:     w.Model,
		Provider:  w.Provider,
		StartTime: w.StartTime,
		TokensIn:  w.TokensIn,
		TokensOut: w.TokensOut,
	}
}

func wireSessionDetailToService(w wireSessionDetail) service.SessionDetail {
	activities := make([]service.ActivityItem, 0, len(w.Activities))
	for _, a := range w.Activities {
		activities = append(activities, service.ActivityItem{
			Label:    a.Label,
			ToolName: a.ToolName,
		})
	}
	return service.SessionDetail{
		Snapshot:       wireSessionSnapshotToService(w.Snapshot),
		SystemPrompt:   w.SystemPrompt,
		InitialMessage: w.InitialMessage,
		Output:         w.Output,
		Activities:     activities,
		WorkerName:     w.WorkerName,
		TeamName:       w.TeamName,
		Task:           w.Task,
	}
}

func wireModelInfoToService(w wireModelInfo) service.ModelInfo {
	return service.ModelInfo{
		ID:                  w.ID,
		Name:                w.Name,
		Provider:            w.Provider,
		State:               w.State,
		MaxContextLength:    w.MaxContextLength,
		LoadedContextLength: w.LoadedContextLength,
	}
}

func wireCatalogProviderToService(w wireCatalogProvider) service.CatalogProvider {
	models := make([]service.CatalogModel, 0, len(w.Models))
	for _, m := range w.Models {
		models = append(models, service.CatalogModel{
			ID:               m.ID,
			Name:             m.Name,
			Family:           m.Family,
			ToolCall:         m.ToolCall,
			Reasoning:        m.Reasoning,
			StructuredOutput: m.StructuredOutput,
			OpenWeights:      m.OpenWeights,
			ContextLimit:     m.ContextLimit,
			OutputLimit:      m.OutputLimit,
			InputCost:        m.InputCost,
			OutputCost:       m.OutputCost,
		})
	}
	return service.CatalogProvider{
		ID:     w.ID,
		Name:   w.Name,
		API:    w.API,
		Doc:    w.Doc,
		Env:    w.Env,
		Models: models,
	}
}

func wireMCPToolInfoToService(w wireMCPToolInfo) service.MCPToolInfo {
	return service.MCPToolInfo{
		NamespacedName: w.NamespacedName,
		OriginalName:   w.OriginalName,
		ServerName:     w.ServerName,
		Description:    w.Description,
		InputSchema:    w.InputSchema,
	}
}

func wireMCPServerStatusToService(w wireMCPServerStatus) service.MCPServerStatus {
	tools := make([]service.MCPToolInfo, 0, len(w.Tools))
	for _, t := range w.Tools {
		tools = append(tools, wireMCPToolInfoToService(t))
	}
	return service.MCPServerStatus{
		Name:      w.Name,
		Transport: w.Transport,
		State:     service.MCPServerState(w.State),
		Error:     w.Error,
		ToolCount: w.ToolCount,
		Tools:     tools,
	}
}

func wireToolCallToService(w wireToolCall) service.ToolCall {
	return service.ToolCall{
		ID:        w.ID,
		Name:      w.Name,
		Arguments: w.Arguments,
	}
}

func wireToolCallResultToService(w wireToolCallResult) service.ToolCallResult {
	return service.ToolCallResult{
		CallID: w.CallID,
		Name:   w.Name,
		Result: w.Result,
		Error:  w.Error,
	}
}

func wireChatMessageToService(w wireChatMessage) service.ChatMessage {
	toolCalls := make([]service.ToolCall, 0, len(w.ToolCalls))
	for _, tc := range w.ToolCalls {
		toolCalls = append(toolCalls, wireToolCallToService(tc))
	}
	return service.ChatMessage{
		Role:       service.MessageRole(w.Role),
		Content:    w.Content,
		ToolCalls:  toolCalls,
		ToolCallID: w.ToolCallID,
	}
}

func wireChatEntryToService(w wireChatEntry) service.ChatEntry {
	return service.ChatEntry{
		Message:    wireChatMessageToService(w.Message),
		Timestamp:  w.Timestamp,
		Reasoning:  w.Reasoning,
		ClaudeMeta: w.ClaudeMeta,
	}
}

func wireWorkerSessionToService(w wireWorkerSession) service.WorkerSession {
	return service.WorkerSession{
		ID:        w.ID,
		WorkerID:  w.WorkerID,
		JobID:     w.JobID,
		TaskID:    w.TaskID,
		Status:    service.SessionStatus(w.Status),
		Model:     w.Model,
		Provider:  w.Provider,
		TokensIn:  w.TokensIn,
		TokensOut: w.TokensOut,
		StartedAt: w.StartedAt,
		EndedAt:   w.EndedAt,
		CostUSD:   w.CostUSD,
	}
}

func wireFeedEntryToService(w wireFeedEntry) service.FeedEntry {
	return service.FeedEntry{
		ID:        w.ID,
		JobID:     w.JobID,
		EntryType: service.FeedEntryType(w.EntryType),
		Content:   w.Content,
		Metadata:  w.Metadata,
		CreatedAt: w.CreatedAt,
	}
}

func wireProgressStateToService(w wireProgressState) service.ProgressState {
	jobs := make([]service.Job, 0, len(w.Jobs))
	for _, j := range w.Jobs {
		jobs = append(jobs, wireJobToService(j))
	}

	tasks := make(map[string][]service.Task, len(w.Tasks))
	for k, v := range w.Tasks {
		st := make([]service.Task, 0, len(v))
		for _, t := range v {
			st = append(st, wireTaskToService(t))
		}
		tasks[k] = st
	}

	reports := make(map[string][]service.ProgressReport, len(w.Reports))
	for k, v := range w.Reports {
		sr := make([]service.ProgressReport, 0, len(v))
		for _, r := range v {
			sr = append(sr, wireProgressReportToService(r))
		}
		reports[k] = sr
	}

	activeSessions := make([]service.WorkerSession, 0, len(w.ActiveSessions))
	for _, s := range w.ActiveSessions {
		activeSessions = append(activeSessions, wireWorkerSessionToService(s))
	}

	liveSnapshots := make([]service.SessionSnapshot, 0, len(w.LiveSnapshots))
	for _, s := range w.LiveSnapshots {
		liveSnapshots = append(liveSnapshots, wireSessionSnapshotToService(s))
	}

	feedEntries := make([]service.FeedEntry, 0, len(w.FeedEntries))
	for _, fe := range w.FeedEntries {
		feedEntries = append(feedEntries, wireFeedEntryToService(fe))
	}

	return service.ProgressState{
		Jobs:           jobs,
		Tasks:          tasks,
		Reports:        reports,
		ActiveSessions: activeSessions,
		LiveSnapshots:  liveSnapshots,
		FeedEntries:    feedEntries,
	}
}

// ---------------------------------------------------------------------------
// parseSSEPayload — inverse of server's eventPayloadToWire
// ---------------------------------------------------------------------------

// parseSSEPayload deserializes a raw JSON payload into the corresponding
// service-level payload type based on the event type. This is the inverse of
// the server's eventPayloadToWire function.
//
// For definitions.reloaded events (which carry no payload), it returns nil, nil.
// For unknown event types, it returns nil, nil (forward-compatible).
func parseSSEPayload(eventType string, raw json.RawMessage) (any, error) {
	// No payload for definitions.reloaded.
	if service.EventType(eventType) == service.EventTypeDefinitionsReloaded {
		return nil, nil
	}

	// Null or empty payload — nothing to decode.
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	switch service.EventType(eventType) {
	case service.EventTypeOperatorText:
		var w wireOperatorTextPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding operator.text payload: %w", err)
		}
		return service.OperatorTextPayload{
			Text:      w.Text,
			Reasoning: w.Reasoning,
		}, nil

	case service.EventTypeOperatorDone:
		var w wireOperatorDonePayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding operator.done payload: %w", err)
		}
		return service.OperatorDonePayload{
			ModelName:       w.ModelName,
			TokensIn:        w.TokensIn,
			TokensOut:       w.TokensOut,
			ReasoningTokens: w.ReasoningTokens,
		}, nil

	case service.EventTypeOperatorPrompt:
		var w wireOperatorPromptPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding operator.prompt payload: %w", err)
		}
		p := service.OperatorPromptPayload{
			RequestID:       w.RequestID,
			Question:        w.Question,
			Options:         w.Options,
			Source:          w.Source,
			ConfirmDispatch: w.ConfirmDispatch,
		}
		if w.PendingDispatch != nil {
			tc := wireToolCallToService(*w.PendingDispatch)
			p.PendingDispatch = &tc
		}
		return p, nil

	case service.EventTypeJobCreated:
		var w wireJobCreatedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding job.created payload: %w", err)
		}
		return service.JobCreatedPayload{
			JobID:       w.JobID,
			Title:       w.Title,
			Description: w.Description,
		}, nil

	case service.EventTypeTaskCreated:
		var w wireTaskCreatedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding task.created payload: %w", err)
		}
		return service.TaskCreatedPayload{
			TaskID: w.TaskID,
			JobID:  w.JobID,
			Title:  w.Title,
			TeamID: w.TeamID,
		}, nil

	case service.EventTypeTaskAssigned:
		var w wireTaskAssignedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding task.assigned payload: %w", err)
		}
		return service.TaskAssignedPayload{
			TaskID: w.TaskID,
			JobID:  w.JobID,
			TeamID: w.TeamID,
			Title:  w.Title,
		}, nil

	case service.EventTypeTaskStarted:
		var w wireTaskStartedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding task.started payload: %w", err)
		}
		return service.TaskStartedPayload{
			TaskID: w.TaskID,
			JobID:  w.JobID,
			TeamID: w.TeamID,
			Title:  w.Title,
		}, nil

	case service.EventTypeTaskCompleted:
		var w wireTaskCompletedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding task.completed payload: %w", err)
		}
		return service.TaskCompletedPayload{
			TaskID:          w.TaskID,
			JobID:           w.JobID,
			TeamID:          w.TeamID,
			Summary:         w.Summary,
			Recommendations: w.Recommendations,
			HasNextTask:     w.HasNextTask,
		}, nil

	case service.EventTypeTaskFailed:
		var w wireTaskFailedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding task.failed payload: %w", err)
		}
		return service.TaskFailedPayload{
			TaskID: w.TaskID,
			JobID:  w.JobID,
			TeamID: w.TeamID,
			Error:  w.Error,
		}, nil

	case service.EventTypeBlockerReported:
		var w wireBlockerReportedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding blocker.reported payload: %w", err)
		}
		return service.BlockerReportedPayload{
			TaskID:      w.TaskID,
			TeamID:      w.TeamID,
			WorkerID:    w.WorkerID,
			Description: w.Description,
			Questions:   w.Questions,
		}, nil

	case service.EventTypeJobCompleted:
		var w wireJobCompletedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding job.completed payload: %w", err)
		}
		return service.JobCompletedPayload{
			JobID:   w.JobID,
			Title:   w.Title,
			Summary: w.Summary,
		}, nil

	case service.EventTypeProgressUpdate:
		var w wireProgressUpdatePayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding progress.update payload: %w", err)
		}
		return service.ProgressUpdatePayload{
			State: wireProgressStateToService(w.State),
		}, nil

	case service.EventTypeSessionStarted:
		var w wireSessionStartedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding session.started payload: %w", err)
		}
		return service.SessionStartedPayload{
			SessionID:      w.SessionID,
			WorkerName:     w.WorkerName,
			TeamName:       w.TeamName,
			Task:           w.Task,
			JobID:          w.JobID,
			TaskID:         w.TaskID,
			SystemPrompt:   w.SystemPrompt,
			InitialMessage: w.InitialMessage,
		}, nil

	case service.EventTypeSessionText:
		var w wireSessionTextPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding session.text payload: %w", err)
		}
		return service.SessionTextPayload{
			Text: w.Text,
		}, nil

	case service.EventTypeSessionToolCall:
		var w wireSessionToolCallPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding session.tool_call payload: %w", err)
		}
		return service.SessionToolCallPayload{
			ToolCall: wireToolCallToService(w.ToolCall),
		}, nil

	case service.EventTypeSessionToolResult:
		var w wireSessionToolResultPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding session.tool_result payload: %w", err)
		}
		return service.SessionToolResultPayload{
			Result: wireToolCallResultToService(w.Result),
		}, nil

	case service.EventTypeSessionDone:
		var w wireSessionDonePayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding session.done payload: %w", err)
		}
		return service.SessionDonePayload{
			WorkerName: w.WorkerName,
			JobID:      w.JobID,
			TaskID:    w.TaskID,
			Status:    w.Status,
			FinalText: w.FinalText,
		}, nil

	case service.EventTypeOperationCompleted:
		var w wireOperationCompletedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding operation.completed payload: %w", err)
		}
		return service.OperationCompletedPayload{
			Kind: w.Kind,
			Result: service.OperationResult{
				OperationID: w.Result.OperationID,
				Content:     w.Result.Content,
				AgentNames:  w.Result.AgentNames,
				Error:       w.Result.Error,
			},
		}, nil

	case service.EventTypeOperationFailed:
		var w wireOperationFailedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding operation.failed payload: %w", err)
		}
		return service.OperationFailedPayload{
			Kind:  w.Kind,
			Error: w.Error,
		}, nil

	case service.EventTypeHeartbeat:
		var w wireHeartbeatPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding heartbeat payload: %w", err)
		}
		return service.HeartbeatPayload{
			ServerTime: w.ServerTime,
		}, nil

	case service.EventTypeConnectionLost:
		var w wireConnectionLostPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding connection.lost payload: %w", err)
		}
		return service.ConnectionLostPayload{
			Error: w.Error,
		}, nil

	case service.EventTypeConnectionRestored:
		return service.ConnectionRestoredPayload{}, nil

	case service.EventTypeGraphNodeStarted:
		var w wireGraphNodeStartedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding graph.node_started payload: %w", err)
		}
		return service.GraphNodeStartedPayload{JobID: w.JobID, TaskID: w.TaskID, Node: w.Node}, nil

	case service.EventTypeGraphNodeCompleted:
		var w wireGraphNodeCompletedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding graph.node_completed payload: %w", err)
		}
		return service.GraphNodeCompletedPayload{JobID: w.JobID, TaskID: w.TaskID, Node: w.Node, Status: w.Status}, nil

	case service.EventTypeGraphCompleted:
		var w wireGraphCompletedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding graph.completed payload: %w", err)
		}
		return service.GraphCompletedPayload{JobID: w.JobID, TaskID: w.TaskID, Summary: w.Summary}, nil

	case service.EventTypeGraphFailed:
		var w wireGraphFailedPayload
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("decoding graph.failed payload: %w", err)
		}
		return service.GraphFailedPayload{JobID: w.JobID, TaskID: w.TaskID, Error: w.Error}, nil

	default:
		// Unknown event type — forward-compatible, ignore payload.
		return nil, nil
	}
}
