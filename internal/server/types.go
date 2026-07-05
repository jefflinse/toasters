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

// CreateSkillRequest is the body for POST /api/v1/skills.
type CreateSkillRequest struct {
	Name string `json:"name"`
}

// GenerateRequest is the body for POST /api/v1/skills/generate.
type GenerateRequest struct {
	Prompt string `json:"prompt"`
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
	Endpoint      string `json:"endpoint"`
	ContextWindow int    `json:"context_window,omitempty"`
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
	WorkerID        string          `json:"worker_id,omitempty"`
	GraphID         string          `json:"graph_id,omitempty"`
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
		WorkerID:        t.WorkerID,
		GraphID:         t.GraphID,
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
	WorkerID  string    `json:"worker_id,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func progressReportToWire(p service.ProgressReport) wireProgressReport {
	return wireProgressReport{
		ID:        p.ID,
		JobID:     p.JobID,
		TaskID:    p.TaskID,
		WorkerID:  p.WorkerID,
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

// wireNoteMeta is the JSON wire representation of a service.NoteMeta, for
// GET /api/v1/jobs/{id}/notes.
type wireNoteMeta struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Source  string    `json:"source,omitempty"`
	ModTime time.Time `json:"mod_time"`
	Size    int64     `json:"size"`
}

func noteMetaToWire(n service.NoteMeta) wireNoteMeta {
	return wireNoteMeta{
		ID:      n.ID,
		Title:   n.Title,
		Source:  n.Source,
		ModTime: n.ModTime,
		Size:    n.Size,
	}
}

// noteContentResponse is the body for GET /api/v1/jobs/{id}/notes/{noteID}.
type noteContentResponse struct {
	Content string `json:"content"`
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

// wireGraphEdge is the JSON wire representation of a service.GraphEdge.
type wireGraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
}

// wireGraphDefinition is the JSON wire representation of a service.GraphDefinition.
type wireGraphDefinition struct {
	ID          string          `json:"id"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Entry       string          `json:"entry"`
	Exit        string          `json:"exit,omitempty"`
	Nodes       []string        `json:"nodes"`
	Edges       []wireGraphEdge `json:"edges,omitempty"`
}

func graphDefinitionToWire(d service.GraphDefinition) wireGraphDefinition {
	out := wireGraphDefinition{
		ID:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Tags:        d.Tags,
		Entry:       d.Entry,
		Exit:        d.Exit,
		Nodes:       d.Nodes,
	}
	for _, e := range d.Edges {
		out.Edges = append(out.Edges, wireGraphEdge{
			From:  e.From,
			To:    e.To,
			Kind:  string(e.Kind),
			Label: e.Label,
		})
	}
	return out
}

// wireSessionSnapshot is the JSON wire representation of a service.SessionSnapshot.
type wireSessionSnapshot struct {
	ID                   string    `json:"id"`
	WorkerID             string    `json:"worker_id"`
	JobID                string    `json:"job_id,omitempty"`
	TaskID               string    `json:"task_id,omitempty"`
	Status               string    `json:"status"`
	Model                string    `json:"model,omitempty"`
	Provider             string    `json:"provider,omitempty"`
	StartTime            time.Time `json:"start_time"`
	TokensIn             int64     `json:"tokens_in"`
	TokensOut            int64     `json:"tokens_out"`
	CurrentContextTokens int64     `json:"current_context_tokens,omitempty"`
	ContextWindow        int       `json:"context_window,omitempty"`
	Compactions          int       `json:"compactions,omitempty"`
}

func sessionSnapshotToWire(s service.SessionSnapshot) wireSessionSnapshot {
	return wireSessionSnapshot{
		ID:                   s.ID,
		WorkerID:             s.WorkerID,
		JobID:                s.JobID,
		TaskID:               s.TaskID,
		Status:               s.Status,
		Model:                s.Model,
		Provider:             s.Provider,
		StartTime:            s.StartTime,
		TokensIn:             s.TokensIn,
		TokensOut:            s.TokensOut,
		CurrentContextTokens: s.CurrentContextTokens,
		ContextWindow:        s.ContextWindow,
		Compactions:          s.Compactions,
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
	WorkerName     string              `json:"worker_name"`
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
		WorkerName:     sd.WorkerName,
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

// wireAddProviderRequest is the JSON wire representation for POST /api/v1/providers.
type wireAddProviderRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Endpoint string `json:"endpoint,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

// wireCatalogProvider is the JSON wire representation of a service.CatalogProvider.
type wireCatalogProvider struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	API    string             `json:"api,omitempty"`
	Doc    string             `json:"doc,omitempty"`
	Env    []string           `json:"env,omitempty"`
	Models []wireCatalogModel `json:"models"`
}

// wireCatalogModel is the JSON wire representation of a service.CatalogModel.
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

func catalogProviderToWire(p service.CatalogProvider) wireCatalogProvider {
	models := make([]wireCatalogModel, 0, len(p.Models))
	for _, m := range p.Models {
		models = append(models, wireCatalogModel{
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
	return wireCatalogProvider{
		ID:     p.ID,
		Name:   p.Name,
		API:    p.API,
		Doc:    p.Doc,
		Env:    p.Env,
		Models: models,
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
	ActiveSessions []wireWorkerSession             `json:"active_sessions"`
	LiveSnapshots  []wireSessionSnapshot           `json:"live_snapshots"`
	GraphNodes     []wireGraphNode                 `json:"active_graph_nodes,omitempty"`
	FeedEntries    []wireFeedEntry                 `json:"feed_entries"`
}

// wireNodeMetric is the JSON wire representation of a service.NodeMetric.
type wireNodeMetric struct {
	Node         string  `json:"node"`
	Runs         int     `json:"runs"`
	Failures     int     `json:"failures"`
	FailureRate  float64 `json:"failure_rate"`
	AvgElapsedMS float64 `json:"avg_elapsed_ms"`
	MinElapsedMS int64   `json:"min_elapsed_ms"`
	MaxElapsedMS int64   `json:"max_elapsed_ms"`
}

func nodeMetricToWire(m service.NodeMetric) wireNodeMetric {
	return wireNodeMetric{
		Node:         m.Node,
		Runs:         m.Runs,
		Failures:     m.Failures,
		FailureRate:  m.FailureRate,
		AvgElapsedMS: m.AvgElapsedMS,
		MinElapsedMS: m.MinElapsedMS,
		MaxElapsedMS: m.MaxElapsedMS,
	}
}

// wireSessionMetric is the JSON wire representation of a service.SessionMetric.
type wireSessionMetric struct {
	WorkerID           string  `json:"worker_id"`
	Sessions           int     `json:"sessions"`
	Failures           int     `json:"failures"`
	FailureRate        float64 `json:"failure_rate"`
	AvgDurationSeconds float64 `json:"avg_duration_seconds"`
	AvgTokensIn        float64 `json:"avg_tokens_in"`
	AvgTokensOut       float64 `json:"avg_tokens_out"`
	UsageUnavailable   int     `json:"usage_unavailable"`
	AvgContextPercent  float64 `json:"avg_context_percent"`
}

func sessionMetricToWire(m service.SessionMetric) wireSessionMetric {
	return wireSessionMetric{
		WorkerID:           m.WorkerID,
		Sessions:           m.Sessions,
		Failures:           m.Failures,
		FailureRate:        m.FailureRate,
		AvgDurationSeconds: m.AvgDurationSeconds,
		AvgTokensIn:        m.AvgTokensIn,
		AvgTokensOut:       m.AvgTokensOut,
		UsageUnavailable:   m.UsageUnavailable,
		AvgContextPercent:  m.AvgContextPercent,
	}
}

// wireMetricsReport is the JSON wire representation of a service.MetricsReport.
type wireMetricsReport struct {
	Nodes    []wireNodeMetric    `json:"nodes"`
	Sessions []wireSessionMetric `json:"sessions"`
}

func metricsReportToWire(r service.MetricsReport) wireMetricsReport {
	nodes := make([]wireNodeMetric, 0, len(r.Nodes))
	for _, m := range r.Nodes {
		nodes = append(nodes, nodeMetricToWire(m))
	}
	sessions := make([]wireSessionMetric, 0, len(r.Sessions))
	for _, m := range r.Sessions {
		sessions = append(sessions, sessionMetricToWire(m))
	}
	return wireMetricsReport{Nodes: nodes, Sessions: sessions}
}

// wireGraphNode is the JSON wire representation of a service.GraphNodeSnapshot.
type wireGraphNode struct {
	SessionID string    `json:"session_id"`
	JobID     string    `json:"job_id"`
	TaskID    string    `json:"task_id"`
	Node      string    `json:"node"`
	StartedAt time.Time `json:"started_at"`
}

// wireWorkerSession is the JSON wire representation of a service.WorkerSession.
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

func workerSessionToWire(s service.WorkerSession) wireWorkerSession {
	return wireWorkerSession{
		ID:        s.ID,
		WorkerID:  s.WorkerID,
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

	activeSessions := make([]wireWorkerSession, 0, len(ps.ActiveSessions))
	for _, s := range ps.ActiveSessions {
		activeSessions = append(activeSessions, workerSessionToWire(s))
	}

	liveSnapshots := make([]wireSessionSnapshot, 0, len(ps.LiveSnapshots))
	for _, s := range ps.LiveSnapshots {
		liveSnapshots = append(liveSnapshots, sessionSnapshotToWire(s))
	}

	graphNodes := make([]wireGraphNode, 0, len(ps.ActiveGraphNodes))
	for _, gn := range ps.ActiveGraphNodes {
		graphNodes = append(graphNodes, wireGraphNode{
			SessionID: gn.SessionID, JobID: gn.JobID, TaskID: gn.TaskID, Node: gn.Node, StartedAt: gn.StartedAt,
		})
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
		GraphNodes:     graphNodes,
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
	ContextTokens   int    `json:"context_tokens,omitempty"`
}

type wireOperatorToolCallPayload struct {
	Name    string          `json:"name"`
	Args    json.RawMessage `json:"args,omitempty"`
	Result  string          `json:"result,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

type wireOperatorCompactionPayload struct {
	BeforeTokens         int    `json:"before_tokens"`
	EstimatedAfterTokens int    `json:"estimated_after_tokens"`
	ArchiveFile          string `json:"archive_file,omitempty"`
}

type wireSessionCompactionPayload struct {
	SessionID            string `json:"session_id"`
	Tier                 int    `json:"tier"`
	BeforeTokens         int    `json:"before_tokens"`
	EstimatedAfterTokens int    `json:"estimated_after_tokens"`
}

type wireBlockerPayload struct {
	RequestID string               `json:"request_id"`
	Source    string               `json:"source,omitempty"`
	JobID     string               `json:"job_id,omitempty"`
	TaskID    string               `json:"task_id,omitempty"`
	Questions []wirePromptQuestion `json:"questions,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

type wireBlockerResolvedPayload struct {
	RequestID   string `json:"request_id"`
	Disposition string `json:"disposition,omitempty"`
}

// wireBlockerRecord is a resolved blocker returned by the history endpoint.
type wireBlockerRecord struct {
	wireBlockerPayload
	ResolvedAt  time.Time `json:"resolved_at"`
	Disposition string    `json:"disposition"`
	Answer      string    `json:"answer,omitempty"`
}

type wirePromptQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

type wireJobCreatedPayload struct {
	JobID       string `json:"job_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type wireTaskCreatedPayload struct {
	TaskID  string `json:"task_id"`
	JobID   string `json:"job_id"`
	Title   string `json:"title"`
	GraphID string `json:"graph_id,omitempty"`
}

type wireTaskAssignedPayload struct {
	TaskID  string `json:"task_id"`
	JobID   string `json:"job_id"`
	GraphID string `json:"graph_id"`
	Title   string `json:"title"`
}

type wireTaskStartedPayload struct {
	TaskID  string `json:"task_id"`
	JobID   string `json:"job_id"`
	GraphID string `json:"graph_id"`
	Title   string `json:"title"`
}

type wireTaskCompletedPayload struct {
	TaskID          string `json:"task_id"`
	JobID           string `json:"job_id"`
	GraphID         string `json:"graph_id"`
	Summary         string `json:"summary"`
	Recommendations string `json:"recommendations,omitempty"`
	HasNextTask     bool   `json:"has_next_task"`
}

type wireTaskFailedPayload struct {
	TaskID  string `json:"task_id"`
	JobID   string `json:"job_id"`
	GraphID string `json:"graph_id"`
	Error   string `json:"error"`
}

type wireJobCompletedPayload struct {
	JobID   string `json:"job_id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`

	Status    string    `json:"status,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	TasksTotal     int `json:"tasks_total,omitempty"`
	TasksCompleted int `json:"tasks_completed,omitempty"`
	TasksFailed    int `json:"tasks_failed,omitempty"`

	TokensIn  int64   `json:"tokens_in,omitempty"`
	TokensOut int64   `json:"tokens_out,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`

	FilesTouched      []wireFileTouch `json:"files_touched,omitempty"`
	FilesTouchedExtra int             `json:"files_touched_extra,omitempty"`
}

type wireFileTouch struct {
	Path  string `json:"path"`
	Size  int64  `json:"size,omitempty"`
	IsNew bool   `json:"is_new,omitempty"`
}

type wireProgressUpdatePayload struct {
	State wireProgressState `json:"state"`
}

type wireSessionStartedPayload struct {
	SessionID      string `json:"session_id"`
	WorkerName     string `json:"worker_name"`
	Task           string `json:"task,omitempty"`
	JobID          string `json:"job_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	SystemPrompt   string `json:"system_prompt,omitempty"`
	InitialMessage string `json:"initial_message,omitempty"`
}

type wireSessionTextPayload struct {
	Text string `json:"text"`
}

type wireSessionReasoningPayload struct {
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

type wireSessionFileChangePayload struct {
	ToolName  string `json:"tool_name"`
	Path      string `json:"path"`
	Diff      string `json:"diff"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	Created   bool   `json:"created,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type wireSessionShellExecPayload struct {
	Command     string `json:"command"`
	ExitCode    int    `json:"exit_code"`
	DurationMs  int64  `json:"duration_ms"`
	OutputBytes int    `json:"output_bytes"`
	Truncated   bool   `json:"truncated,omitempty"`
	TimedOut    bool   `json:"timed_out,omitempty"`
}

type wireSessionWorkerSpawnPayload struct {
	Role   string `json:"role"`
	Task   string `json:"task,omitempty"`
	JobID  string `json:"job_id,omitempty"`
	Depth  int    `json:"depth,omitempty"`
	Failed bool   `json:"failed,omitempty"`
	Error  string `json:"error,omitempty"`
}

type wireSessionKBPayload struct {
	Scope   string `json:"scope"`
	Op      string `json:"op"`
	Source  string `json:"source,omitempty"`
	Preview string `json:"preview,omitempty"`
}

type wireSessionDonePayload struct {
	WorkerName string `json:"worker_name"`
	JobID      string `json:"job_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	Status     string `json:"status"`
	FinalText  string `json:"final_text,omitempty"`
}

type wireSessionPromptPayload struct {
	SessionID      string `json:"session_id"`
	SystemPrompt   string `json:"system_prompt,omitempty"`
	InitialMessage string `json:"initial_message,omitempty"`
}

type wireSessionMetaPayload struct {
	SessionID   string  `json:"session_id"`
	Model       string  `json:"model,omitempty"`
	Provider    string  `json:"provider,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	Thinking    bool    `json:"thinking,omitempty"`
}

type wireSessionContextPayload struct {
	SessionID     string `json:"session_id"`
	ContextTokens int64  `json:"context_tokens"`
}

type wireOperationCompletedPayload struct {
	Kind   string              `json:"kind"`
	Result wireOperationResult `json:"result"`
}

type wireOperationResult struct {
	OperationID string `json:"operation_id"`
	Content     string `json:"content,omitempty"`
	Error       string `json:"error,omitempty"`
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

// EventPayloadToWire converts a service event payload to its wire representation.
func EventPayloadToWire(ev service.Event) any {
	switch p := ev.Payload.(type) {
	case service.OperatorTextPayload:
		return wireOperatorTextPayload{Text: p.Text, Reasoning: p.Reasoning}
	case service.OperatorToolCallPayload:
		return wireOperatorToolCallPayload{Name: p.Name, Args: p.Args, Result: p.Result, IsError: p.IsError}
	case service.OperatorDonePayload:
		return wireOperatorDonePayload{
			ModelName: p.ModelName, TokensIn: p.TokensIn,
			TokensOut: p.TokensOut, ReasoningTokens: p.ReasoningTokens,
			ContextTokens: p.ContextTokens,
		}
	case service.OperatorCompactionPayload:
		return wireOperatorCompactionPayload{
			BeforeTokens:         p.BeforeTokens,
			EstimatedAfterTokens: p.EstimatedAfterTokens,
			ArchiveFile:          p.ArchiveFile,
		}
	case service.SessionCompactionPayload:
		return wireSessionCompactionPayload{
			SessionID:            p.SessionID,
			Tier:                 p.Tier,
			BeforeTokens:         p.BeforeTokens,
			EstimatedAfterTokens: p.EstimatedAfterTokens,
		}
	case service.Blocker:
		w := wireBlockerPayload{
			RequestID: p.RequestID,
			Source:    p.Source,
			JobID:     p.JobID,
			TaskID:    p.TaskID,
			CreatedAt: p.CreatedAt,
		}
		for _, q := range p.Questions {
			w.Questions = append(w.Questions, wirePromptQuestion{Question: q.Question, Options: q.Options})
		}
		return w
	case service.BlockerResolvedPayload:
		return wireBlockerResolvedPayload{RequestID: p.RequestID, Disposition: p.Disposition}
	case service.JobCreatedPayload:
		return wireJobCreatedPayload{JobID: p.JobID, Title: p.Title, Description: p.Description}
	case service.TaskCreatedPayload:
		return wireTaskCreatedPayload{TaskID: p.TaskID, JobID: p.JobID, Title: p.Title, GraphID: p.GraphID}
	case service.TaskAssignedPayload:
		return wireTaskAssignedPayload{TaskID: p.TaskID, JobID: p.JobID, GraphID: p.GraphID, Title: p.Title}
	case service.TaskStartedPayload:
		return wireTaskStartedPayload{TaskID: p.TaskID, JobID: p.JobID, GraphID: p.GraphID, Title: p.Title}
	case service.TaskCompletedPayload:
		return wireTaskCompletedPayload{
			TaskID: p.TaskID, JobID: p.JobID, GraphID: p.GraphID,
			Summary: p.Summary, Recommendations: p.Recommendations, HasNextTask: p.HasNextTask,
		}
	case service.TaskFailedPayload:
		return wireTaskFailedPayload{TaskID: p.TaskID, JobID: p.JobID, GraphID: p.GraphID, Error: p.Error}
	case service.JobCompletedPayload:
		files := make([]wireFileTouch, 0, len(p.FilesTouched))
		for _, f := range p.FilesTouched {
			files = append(files, wireFileTouch{Path: f.Path, Size: f.Size, IsNew: f.IsNew})
		}
		return wireJobCompletedPayload{
			JobID: p.JobID, Title: p.Title, Summary: p.Summary,
			Status:            string(p.Status),
			Workspace:         p.Workspace,
			StartedAt:         p.StartedAt,
			EndedAt:           p.EndedAt,
			TasksTotal:        p.TasksTotal,
			TasksCompleted:    p.TasksCompleted,
			TasksFailed:       p.TasksFailed,
			TokensIn:          p.TokensIn,
			TokensOut:         p.TokensOut,
			CostUSD:           p.CostUSD,
			FilesTouched:      files,
			FilesTouchedExtra: p.FilesTouchedExtra,
		}
	case service.ProgressUpdatePayload:
		return wireProgressUpdatePayload{State: progressStateToWire(p.State)}
	case service.SessionStartedPayload:
		return wireSessionStartedPayload{
			SessionID: p.SessionID, WorkerName: p.WorkerName,
			Task: p.Task, JobID: p.JobID, TaskID: p.TaskID,
			SystemPrompt: p.SystemPrompt, InitialMessage: p.InitialMessage,
		}
	case service.SessionTextPayload:
		return wireSessionTextPayload{Text: p.Text}
	case service.SessionReasoningPayload:
		return wireSessionReasoningPayload{Text: p.Text}
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
	case service.SessionFileChangePayload:
		return wireSessionFileChangePayload{
			ToolName: p.ToolName, Path: p.Path, Diff: p.Diff,
			Added: p.Added, Removed: p.Removed,
			Created: p.Created, Truncated: p.Truncated,
		}
	case service.SessionShellExecPayload:
		return wireSessionShellExecPayload{
			Command: p.Command, ExitCode: p.ExitCode, DurationMs: p.DurationMs,
			OutputBytes: p.OutputBytes, Truncated: p.Truncated, TimedOut: p.TimedOut,
		}
	case service.SessionWorkerSpawnPayload:
		return wireSessionWorkerSpawnPayload{
			Role: p.Role, Task: p.Task, JobID: p.JobID,
			Depth: p.Depth, Failed: p.Failed, Error: p.Error,
		}
	case service.SessionKBPayload:
		return wireSessionKBPayload{
			Scope: p.Scope, Op: p.Op, Source: p.Source, Preview: p.Preview,
		}
	case service.SessionDonePayload:
		return wireSessionDonePayload{
			WorkerName: p.WorkerName, JobID: p.JobID, TaskID: p.TaskID,
			Status: p.Status, FinalText: p.FinalText,
		}
	case service.SessionPromptPayload:
		return wireSessionPromptPayload{
			SessionID: p.SessionID, SystemPrompt: p.SystemPrompt, InitialMessage: p.InitialMessage,
		}
	case service.SessionMetaPayload:
		return wireSessionMetaPayload{
			SessionID: p.SessionID, Model: p.Model, Provider: p.Provider,
			Temperature: p.Temperature, Thinking: p.Thinking,
		}
	case service.SessionContextPayload:
		return wireSessionContextPayload{
			SessionID: p.SessionID, ContextTokens: p.ContextTokens,
		}
	case service.OperationCompletedPayload:
		return wireOperationCompletedPayload{
			Kind: p.Kind,
			Result: wireOperationResult{
				OperationID: p.Result.OperationID, Content: p.Result.Content,
				Error: p.Result.Error,
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
	case service.GraphNodeStartedPayload:
		return wireGraphNodeStartedPayload{JobID: p.JobID, TaskID: p.TaskID, Node: p.Node}
	case service.GraphNodeCompletedPayload:
		return wireGraphNodeCompletedPayload{JobID: p.JobID, TaskID: p.TaskID, Node: p.Node, Status: p.Status}
	case service.GraphCompletedPayload:
		return wireGraphCompletedPayload{JobID: p.JobID, TaskID: p.TaskID, Summary: p.Summary}
	case service.GraphFailedPayload:
		return wireGraphFailedPayload{JobID: p.JobID, TaskID: p.TaskID, Error: p.Error}
	default:
		return ev.Payload
	}
}
