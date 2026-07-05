package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jefflinse/toasters/internal/tooldef"
)

// SpawnOpts configures a new worker session.
type SpawnOpts struct {
	WorkerID        string
	ProviderName    string
	Model           string
	SystemPrompt    string
	Tools           []ToolDef
	DisallowedTools []string     // tool names to deny at CoreTools level (defense-in-depth)
	ToolExecutor    ToolExecutor // optional; overrides default CoreTools when set
	ExtraTools      ToolExecutor // optional; layered on top of CoreTools (overlay with dispatch priority)
	JobID           string
	TaskID          string
	TeamName        string // team this worker belongs to (may be empty)
	Task            string // short human-readable description of what this worker is doing (≤60 chars)
	InitialMessage  string
	WorkDir         string
	MaxTurns        int  // 0 = use default (50)
	MaxDepth        int  // 0 = use default (1); coordinators may spawn workers, workers may not spawn further
	Depth           int  // current spawn depth (set by parent)
	Hidden          bool // when true, OnSessionStarted is not called (internal/system sessions)
}

const (
	defaultMaxTurns = 50
	defaultMaxDepth = 1
)

// ToolDef is an alias for tooldef.ToolDef, the shared tool definition type.
type ToolDef = tooldef.ToolDef

// MCPCaller is an alias for tooldef.MCPCaller, the shared MCP dispatch interface.
type MCPCaller = tooldef.MCPCaller

// SessionSnapshot is a read-only view of a session's state.
type SessionSnapshot struct {
	ID        string
	WorkerID  string
	TeamName  string // team this worker belongs to (may be empty)
	JobID     string
	TaskID    string
	Status    string
	Model     string
	Provider  string
	StartTime time.Time
	TokensIn  int64
	TokensOut int64
	// CurrentContextTokens is the prompt size of the most recent round-trip —
	// the session's live context-window occupancy (0 if none reported yet).
	CurrentContextTokens int64
	// Compactions is how many history compactions this session has
	// performed (drives the fleet row's ↺n badge, reconnect-safe).
	Compactions int
}

// SessionEvent is emitted by a session for observers.
type SessionEvent struct {
	SessionID   string
	Type        SessionEventType
	Text        string
	ToolCall    *ToolCallEvent
	ToolResult  *ToolResultEvent
	FileChange  *FileChange
	ShellExec   *ShellExec
	WorkerSpawn *WorkerSpawn
	Compaction  *CompactionEvent
	KBNote      *KBNote
	Error       error
}

// SessionEventType identifies the kind of session event.
type SessionEventType string

const (
	SessionEventText        SessionEventType = "text"
	SessionEventToolCall    SessionEventType = "tool_call"
	SessionEventToolResult  SessionEventType = "tool_result"
	SessionEventFileChange  SessionEventType = "file_change"
	SessionEventShellExec   SessionEventType = "shell_exec"
	SessionEventWorkerSpawn SessionEventType = "worker_spawn"
	SessionEventCompaction  SessionEventType = "compaction"
	SessionEventKBNote      SessionEventType = "kb_note"
	SessionEventDone        SessionEventType = "done"
	SessionEventError       SessionEventType = "error"
)

// CompactionEvent describes a history compaction performed by a session.
type CompactionEvent struct {
	// Tier is 1 (tool-result elision) or 2 (summarize-and-continue).
	Tier int
	// BeforeTokens is the occupancy that triggered the compaction (the
	// provider-reported value for pre-flight, or the estimate for the
	// overflow backstop).
	BeforeTokens int
	// EstimatedAfterTokens is the bytes/4 estimate of the compacted
	// history; the next round-trip reports the exact value.
	EstimatedAfterTokens int
}

// ToolCallEvent describes a tool invocation by the LLM.
type ToolCallEvent struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResultEvent describes the result of a tool execution.
type ToolResultEvent struct {
	CallID string
	Name   string
	Result string
	Error  string
}

// FileChange describes a file mutation performed by a built-in file tool
// (write_file / edit_file). It exists for display: the diff is shown to the
// user but deliberately kept OUT of the tool result string returned to the
// LLM, which already knows what it wrote and shouldn't spend context on it.
type FileChange struct {
	ToolName  string // "write_file" or "edit_file"
	Path      string // path as the model passed it (pre-resolution)
	Diff      string // unified diff body (hunks only, no ---/+++ header), capped
	Added     int    // total lines added (across the whole change, not the capped diff)
	Removed   int    // total lines removed
	Created   bool   // true when write_file created a new file
	Truncated bool   // true when Diff was capped server-side
}

// FileChangeNotifier receives FileChange notifications from CoreTools as a
// display side-channel. The ctx is the one passed to Execute, so callers that
// stash per-invocation identity in ctx (graphexec's NodeContext) can recover
// it at notification time.
type FileChangeNotifier func(ctx context.Context, fc FileChange)

// ShellExec describes a command executed by the built-in shell tool. It
// exists for display, mirroring FileChange: the model already sees the
// command's output in its own tool result (subject to the session's result
// cap), so this side-channel carries only the structured metadata — exit
// code, timing, size — that would otherwise have to be re-parsed out of the
// result text in the TUI.
type ShellExec struct {
	Command     string // command as issued, capped for display (maxShellExecCommandBytes)
	ExitCode    int    // process exit code; -1 when unavailable (killed by signal, never started)
	DurationMs  int64  // wall-clock time spent running the command
	OutputBytes int    // combined stdout+stderr size, before any truncation
	// Truncated is true when the model-visible result would exceed the
	// standard tool-result cap (session.go's 8KB limit). For runtime.Session
	// workers this is exact — same constant, same condition. For graph nodes
	// (which don't run that truncation loop) it's an approximation: "large
	// enough that the ordinary path would have truncated it."
	Truncated bool
	TimedOut  bool // true when the command was killed after exceeding its timeout
}

// ShellExecNotifier receives ShellExec notifications from CoreTools as a
// display side-channel. See FileChangeNotifier for the ctx contract.
type ShellExecNotifier func(ctx context.Context, se ShellExec)

// WorkerSpawn describes an attempt by the built-in spawn_worker tool to
// start a child worker session. It exists for display, mirroring ShellExec:
// the model already gets the child's own final text (or a failure message)
// back as the spawn_worker tool result, so this side-channel carries only
// the structured metadata — which role, for what task, at what depth, and
// whether the spawn itself succeeded — needed to render a compact spawn
// card on the parent's spawn_worker tool block.
//
// Unlike ShellExec, notifications fire for every spawnWorker exit path,
// including the pre-attempt validation failures (no spawner attached, depth
// limit exceeded, unknown role): a rejected spawn_worker call is itself
// meaningful information for the card, whereas shell's validation errors
// (e.g. "shell tool is disabled") never ran a command and have nothing to
// report.
type WorkerSpawn struct {
	Role  string // requested role/worker name
	Task  string // task label/description, capped for display (maxWorkerSpawnTaskBytes)
	JobID string // job the parent (and child) belong to
	Depth int    // spawn depth the child would run at (parent depth + 1)

	Failed bool
	Error  string // capped failure message (maxWorkerSpawnErrorBytes); empty on success
}

// WorkerSpawnNotifier receives WorkerSpawn notifications from CoreTools as a
// display side-channel. See FileChangeNotifier for the ctx contract.
type WorkerSpawnNotifier func(ctx context.Context, ws WorkerSpawn)

// KBNote describes a job-note tool call (job_note_write or job_notes_search)
// made by a worker. It exists for display, mirroring ShellExec/WorkerSpawn:
// the model already sees the tool's own result text (the saved note's id, or
// the search hits), so this side-channel carries only the structured
// metadata — scope, operation, source, and a short preview — that the TUI's
// Knowledge screen and fleet activity feed need. See docs/kb-design.md's
// "Observability" section.
type KBNote struct {
	Scope   string // "job" for job notes; reserved for user/system scopes later
	Op      string // "write" or "search"
	Source  string // the worker's note-source label (see CoreTools.noteSource)
	Preview string // write: title + id; search: query (or "list") + hit count
}

// KBNoteNotifier receives KBNote notifications from CoreTools as a display
// side-channel. See FileChangeNotifier for the ctx contract.
type KBNoteNotifier func(ctx context.Context, kb KBNote)
