// Package service provides the in-process implementation of the Service interface.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/hitl"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// Compile-time assertion that LocalService satisfies the Service interface.
var _ Service = (*LocalService)(nil)

// Size limits for input validation.
const (
	maxMessageLen  = 102400 // 100KB — maximum user message size
	maxPromptLen   = 51200  // 50KB — maximum generation prompt size
	maxResponseLen = 51200  // 50KB — maximum prompt/blocker response size
)

// maxConcurrentOps bounds the number of concurrent async operations (generate,
// promote, detect) that can run simultaneously.
const maxConcurrentOps = 5

// maxHistoryEntries bounds the conversation history kept for reconnect hydration.
const maxHistoryEntries = 1000

// LocalConfig holds the dependencies for LocalService.
//
// Operator, Provider, OperatorModel, OperatorEndpoint, GraphExecutor,
// DefaultProvider, and DefaultModel are initial values only: live operator
// activation (startOperator) replaces them at runtime, so they are copied
// into opMu-guarded fields at construction and never read from cfg again.
type LocalConfig struct {
	// AppConfig is the loaded application config, used for serving and
	// mutating user-editable settings (see GetSettings/UpdateSettings).
	// Optional — if nil, the settings endpoints return defaults and
	// UpdateSettings is a no-op that returns an error.
	AppConfig        *config.Config
	Store            db.Store
	Runtime          *runtime.Runtime
	Operator         *operator.Operator
	MCPManager       *mcp.Manager
	Provider         provider.Provider // operator's LLM provider (for ListModels, generation)
	DefaultProvider  string            // default provider for system workers
	DefaultModel     string            // default model for system workers
	Loader           *loader.Loader
	ConfigDir        string
	WorkspaceDir     string
	OperatorModel    string                     // for OperatorStatus.ModelName
	OperatorEndpoint string                     // for OperatorStatus.Endpoint (LLM provider URL)
	StartTime        time.Time                  // for Health().Uptime
	Catalog          CatalogSource              // optional models.dev catalog; nil disables ListCatalogProviders
	Registry         *provider.Registry         // provider registry for live operator activation
	PromptEngine     *prompt.Engine             // optional; for role-based prompt composition
	GraphExecutor    operator.GraphTaskExecutor // optional; rhizome graph-based task execution
	GraphCatalog     operator.GraphCatalog      // optional; backs query_graphs on the live-activated operator

	// OperatorProviderID is the operator's provider registry key ("lmstudio"),
	// distinct from OperatorModel/OperatorEndpoint. Live activation
	// (startOperator) replaces it like the other operator fields.
	OperatorProviderID string

	// ContextWindows resolves effective context windows for provider/model
	// pairs. Optional — nil leaves DTO ContextWindow fields at 0 ("unknown").
	ContextWindows ContextWindowSource
}

// ContextWindowSource resolves effective context windows and ingests
// provider-reported model lists. *contextwindow.Resolver satisfies it; the
// interface lives here so the service doesn't depend on the resolver package.
type ContextWindowSource interface {
	Window(ctx context.Context, providerName, modelID string) int
	ObserveModels(providerKey string, models []provider.ModelInfo)
}

// LocalService is the in-process implementation of Service. It delegates to
// existing internal components (db.Store, operator.Operator, runtime.Runtime,
// mcp.Manager, etc.) and multiplexes events from all sources into a single
// channel per subscriber.
type LocalService struct {
	cfg LocalConfig

	// Service lifetime context — cancelled by Shutdown().
	ctx    context.Context
	cancel context.CancelFunc

	// Event stream state.
	mu          sync.Mutex
	subscribers map[uint64]chan Event
	nextSubID   uint64
	seqCounter  uint64 // protected by mu
	startOnce   sync.Once

	// Operator turn correlation.
	turnMu           sync.Mutex
	currentTurnID    string
	pendingResponse  strings.Builder // accumulates text during a turn
	pendingReasoning strings.Builder // accumulates reasoning during a turn (persisted with the chat entry)

	// asyncSem bounds concurrent async operations (generate, promote, detect).
	asyncSem chan struct{}

	// Operator lifecycle — for live activation. startOperator (PUT
	// /api/v1/operator/provider) replaces all of this state while other
	// HTTP requests read it, so every access goes through opMu. The
	// corresponding LocalConfig fields are initial values only — runtime
	// reads must use the accessors (currentOperator, currentProvider,
	// operatorInfo, currentGraphExecutor, currentDefaults).
	opMu            sync.Mutex
	opCancel        context.CancelFunc // cancels the running operator; nil if no operator
	op              *operator.Operator
	opProvider      provider.Provider
	opProviderID    string
	opModel         string
	opEndpoint      string
	graphExec       operator.GraphTaskExecutor
	defaultProvider string
	defaultModel    string

	// broker coordinates HITL prompt/response for both the operator's
	// ask_user tool and any graph node that calls rhizome.Interrupt.
	broker *hitl.Broker

	// blockers holds pending ask_user requests that have not yet been answered.
	// Keyed by RequestID. Populated when the operator/a graph node calls
	// ask_user; removed when the waiter's broker.Ask returns (answered or
	// cancelled). Guarded by blockerMu.
	blockerMu sync.Mutex
	blockers  map[string]Blocker
	// blockerOutcomes records the answer/disposition delivered via
	// RespondToPrompt/DismissPrompt, keyed by RequestID, so ResolveBlocker —
	// which only receives the request ID from the waiter's return path — can
	// persist how the blocker resolved. An ID with no recorded outcome
	// resolved by cancellation (waiter's ctx ended). Guarded by blockerMu.
	blockerOutcomes map[string]blockerOutcome

	// activeGraphNodes tracks currently-executing graph nodes, keyed by their
	// session id ("graph:<taskID>:<node>"). Graph nodes run via the graph engine
	// (not runtime worker sessions), so they aren't in Runtime.ActiveSessions;
	// tracking them here lets a reconnecting client rebuild the Workers panel for
	// an in-flight graph job from the progress snapshot. Guarded by graphNodeMu.
	graphNodeMu      sync.Mutex
	activeGraphNodes map[string]GraphNodeSnapshot
}

// localJobService wraps LocalService to implement JobService without conflicting
// with SessionService methods of the same name (List, Get, Cancel).
type localJobService struct{ svc *LocalService }

// localSessionService wraps LocalService to implement SessionService without
// conflicting with JobService methods of the same name (List, Get, Cancel).
type localSessionService struct{ svc *LocalService }

// nameReplacer strips characters that could cause YAML injection when
// interpolated into frontmatter templates.
var nameReplacer = strings.NewReplacer(
	"\n", "",
	"\r", "",
	"\x00", "",
	":", "",
	"#", "",
	"\"", "",
	"'", "",
	"{", "",
	"}", "",
	"[", "",
	"]", "",
	"|", "",
	">", "",
)

// sanitizeName strips characters that could cause YAML injection when
// interpolated into frontmatter templates.
func sanitizeName(name string) string {
	return nameReplacer.Replace(name)
}

// NewLocal creates a new LocalService from the given config.
func NewLocal(cfg LocalConfig) *LocalService {
	if cfg.StartTime.IsZero() {
		cfg.StartTime = time.Now()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &LocalService{
		cfg:              cfg,
		ctx:              ctx,
		cancel:           cancel,
		subscribers:      make(map[uint64]chan Event),
		asyncSem:         make(chan struct{}, maxConcurrentOps),
		broker:           hitl.New(),
		blockers:         make(map[string]Blocker),
		blockerOutcomes:  make(map[string]blockerOutcome),
		activeGraphNodes: make(map[string]GraphNodeSnapshot),
		op:               cfg.Operator,
		opProvider:       cfg.Provider,
		opProviderID:     cfg.OperatorProviderID,
		opModel:          cfg.OperatorModel,
		opEndpoint:       cfg.OperatorEndpoint,
		graphExec:        cfg.GraphExecutor,
		defaultProvider:  cfg.DefaultProvider,
		defaultModel:     cfg.DefaultModel,
	}
}

// Broker exposes the HITL broker so the operator and graph executor can
// register pending prompts with a single shared coordinator.
func (s *LocalService) Broker() *hitl.Broker { return s.broker }

// Shutdown cancels the service lifetime context, stopping background
// goroutines, then waits for detached graph dispatch goroutines to persist
// their terminal task status. The wait must happen here — before the caller's
// deferred store.Close() runs — or the dispatchers race the closing database
// and tasks are stranded in_progress.
func (s *LocalService) Shutdown() {
	s.cancel()
	if d, ok := s.currentGraphExecutor().(interface{ Drain(time.Duration) bool }); ok {
		if !d.Drain(15 * time.Second) {
			slog.Warn("graph executor drain timed out; in-flight tasks may be left in_progress")
		}
	}
}

// Ctx returns the service-level lifetime context. It is cancelled by
// Shutdown. External code (e.g. cmd/serve.go constructing an operator
// before calling SetOperator) should pass this as operator.Config.LifetimeCtx
// so detached graph dispatch goroutines are bounded by service lifetime.
func (s *LocalService) Ctx() context.Context { return s.ctx }

// tryAcquireAsync attempts to acquire a slot for an async operation.
// Returns false if the semaphore is full (too many concurrent operations).
func (s *LocalService) tryAcquireAsync() bool {
	select {
	case s.asyncSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseAsync releases a slot after an async operation completes.
func (s *LocalService) releaseAsync() {
	<-s.asyncSem
}

// safeGo launches fn in a goroutine with panic recovery. If fn panics,
// the stack trace is logged and an operation.failed event is broadcast.
func (s *LocalService) safeGo(operationID, kind string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				slog.Error("panic in async operation",
					"operation_id", operationID,
					"kind", kind,
					"panic", fmt.Sprintf("%v", r),
					"stack", string(stack),
				)
				s.broadcast(Event{
					Type:        EventTypeOperationFailed,
					OperationID: operationID,
					Payload: OperationFailedPayload{
						Kind:  kind,
						Error: "internal error: unexpected panic",
					},
				})
			}
		}()
		fn()
	}()
}

// ---------------------------------------------------------------------------
// Sub-interface accessors
// ---------------------------------------------------------------------------

func (s *LocalService) Operator() OperatorService      { return s }
func (s *LocalService) Definitions() DefinitionService { return s }
func (s *LocalService) Jobs() JobService               { return &localJobService{s} }
func (s *LocalService) Sessions() SessionService       { return &localSessionService{s} }
func (s *LocalService) Events() EventService           { return s }
func (s *LocalService) System() SystemService          { return s }
