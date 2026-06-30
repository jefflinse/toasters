package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// operatorTools implements runtime.ToolExecutor for the operator's tool set.
// It provides consult_worker (spawn a system worker), surface_to_user (relay
// information to the user), query_job, and query_graphs.
type operatorTools struct {
	rt              *runtime.Runtime
	promptEngine    *prompt.Engine
	defaultProvider string
	defaultModel    string
	store           db.Store
	systemTools     *SystemTools
	workDir         string
	promptUser      func(ctx context.Context, requestID string, questions []graphexec.PromptQuestion) (string, error) // set by Operator after construction
}

func newOperatorTools(rt *runtime.Runtime, promptEngine *prompt.Engine, defaultProvider, defaultModel string, store db.Store, systemTools *SystemTools, workDir string) *operatorTools {
	return &operatorTools{
		rt:              rt,
		promptEngine:    promptEngine,
		defaultProvider: defaultProvider,
		defaultModel:    defaultModel,
		store:           store,
		systemTools:     systemTools,
		workDir:         workDir,
	}
}

// Definitions returns the tool definitions available to the operator LLM.
func (ot *operatorTools) Definitions() []runtime.ToolDef {
	defs := []runtime.ToolDef{
		{
			Name:        "surface_to_user",
			Description: "Surface information to the user. Use this to relay important findings, summaries, or status updates that the user should see.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"text": {
						"type": "string",
						"description": "The text to show to the user"
					}
				},
				"required": ["text"]
			}`),
		},
		{
			Name:        "query_job",
			Description: "Get the current state of a job including all its tasks and their statuses.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": "ID of the job to query"
					}
				},
				"required": ["job_id"]
			}`),
		},
		{
			Name:        "list_jobs",
			Description: "List all jobs with their IDs, titles, statuses, and workspace directories. Use this to find a job by name when you don't have its ID.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
		{
			Name:        "query_graphs",
			Description: "List all available graphs with their ids, names, descriptions, and tags. Graphs are declarative, user-defined pipelines that execute a specific class of work — pick one before creating a task to target it.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
		{
			Name:        "setup_workspace",
			Description: "Clone one or more git repositories into the job's workspace directory before decomposition. Sets the job status to setting_up while running. Returns the workspace path and a summary of what was cloned.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": "The ID of the job whose workspace should be set up"
					},
					"repos": {
						"type": "array",
						"description": "List of repositories to clone",
						"items": {
							"type": "object",
							"properties": {
								"url": {"type": "string", "description": "Git repository URL to clone"},
								"name": {"type": "string", "description": "Directory name to clone into (defaults to repo name from URL if omitted)"}
							},
							"required": ["url"]
						}
					}
				},
				"required": ["job_id", "repos"]
			}`),
		},
	}

	defs = append(defs, runtime.ToolDef{
		Name:        "ask_user",
		Description: "Ask the user one or more questions and wait for their answers. Use this whenever you need clarification, confirmation, or a decision. ALWAYS use this tool instead of writing questions as prose — never list clarifying questions in your text response. To ask several things at once (e.g. a round of clarifying questions), pass them all in `questions`; the user answers them together in a single exchange. Provide suggested `options` for each question whenever possible so the user can answer with one selection.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"questions": {
					"type": "array",
					"description": "The questions to ask, presented to the user as one round. Prefer this over the single-question shorthand whenever you have more than one thing to ask.",
					"items": {
						"type": "object",
						"properties": {
							"question": {"type": "string", "description": "The question text"},
							"options": {
								"type": "array",
								"items": {"type": "string"},
								"description": "Optional suggested answers. The user can also type a custom response."
							}
						},
						"required": ["question"]
					}
				},
				"question": {
					"type": "string",
					"description": "A single question to ask. Convenience shorthand for a one-question round; prefer the questions array when asking more than one thing."
				},
				"options": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Optional suggested answers for the single question. The user can also type a custom response."
				}
			}
		}`),
	})

	// Append job/task tools from SystemTools. Graph assignment is not
	// operator-driven — fine-decompose selects graphs automatically — but the
	// operator can add follow-up tasks to an existing job with create_task
	// (e.g. when a graph requests new work via new_task_request).
	wantFromSystem := map[string]bool{"create_job": true, "create_task": true, "retry_task": true}
	for _, td := range ot.systemTools.Definitions() {
		if wantFromSystem[td.Name] {
			defs = append(defs, td)
		}
	}

	return defs
}

// Execute dispatches a tool call by name.
func (ot *operatorTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "surface_to_user":
		return ot.surfaceToUser(ctx, args)
	case "list_jobs":
		return ot.listJobs(ctx)
	case "query_job":
		return ot.queryJob(ctx, args)
	case "query_graphs":
		return ot.queryGraphs(ctx)
	case "setup_workspace":
		return ot.setupWorkspace(ctx, args)
	case "create_job":
		return ot.systemTools.Execute(ctx, "create_job", args)
	case "create_task":
		return ot.systemTools.Execute(ctx, "create_task", args)
	case "retry_task":
		return ot.systemTools.Execute(ctx, "retry_task", args)
	case "ask_user":
		return ot.askUser(ctx, args)
	default:
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
}

func (ot *operatorTools) surfaceToUser(ctx context.Context, args json.RawMessage) (string, error) {
	if ot.systemTools == nil {
		return "", fmt.Errorf("surface_to_user unavailable: no system tools configured")
	}
	return ot.systemTools.Execute(ctx, "surface_to_user", args)
}

// listJobs returns all jobs with their IDs, titles, statuses, and workspace directories.
func (ot *operatorTools) listJobs(ctx context.Context) (string, error) {
	jobs, err := ot.store.ListAllJobs(ctx)
	if err != nil {
		return "", fmt.Errorf("listing jobs: %w", err)
	}

	if len(jobs) == 0 {
		return "No jobs.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Jobs (%d):\n", len(jobs))
	for _, job := range jobs {
		fmt.Fprintf(&b, "\n- %s (id: %s)\n", job.Title, job.ID)
		fmt.Fprintf(&b, "  Status: %s\n", job.Status)
		if job.WorkspaceDir != "" {
			fmt.Fprintf(&b, "  Workspace: %s\n", contractHome(job.WorkspaceDir))
		}
	}

	return b.String(), nil
}

// queryJob delegates to SystemTools.queryJob for DB-backed job queries.
func (ot *operatorTools) queryJob(ctx context.Context, args json.RawMessage) (string, error) {
	if ot.systemTools == nil {
		return "", fmt.Errorf("query_job unavailable: no system tools configured")
	}
	return ot.systemTools.Execute(ctx, "query_job", args)
}

// queryGraphs delegates to SystemTools.queryGraphs for catalog queries.
func (ot *operatorTools) queryGraphs(ctx context.Context) (string, error) {
	if ot.systemTools == nil {
		return "", fmt.Errorf("query_graphs unavailable: no system tools configured")
	}
	return ot.systemTools.Execute(ctx, "query_graphs", json.RawMessage(`{}`))
}

func (ot *operatorTools) askUser(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Questions json.RawMessage `json:"questions"`
		Question  string          `json:"question"`
		Options   []string        `json:"options"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing ask_user args: %w", err)
	}

	questions, err := graphexec.ParsePromptQuestions(params.Questions)
	if err != nil {
		return "", fmt.Errorf("parsing ask_user questions: %w", err)
	}
	if len(questions) == 0 && params.Question != "" {
		// Single-question shorthand — but the model sometimes packs the whole
		// questions array into this string field too, so route it through the
		// same lenient/recursive parser.
		qs, perr := graphexec.QuestionsFromString(params.Question)
		if perr == nil && len(qs) > 0 {
			questions = qs
			if len(questions) == 1 && len(questions[0].Options) == 0 {
				questions[0].Options = params.Options
			}
		}
	}
	if len(questions) == 0 {
		return "", fmt.Errorf("ask_user requires at least one question")
	}
	for i, q := range questions {
		if q.Question == "" {
			return "", fmt.Errorf("question %d is empty", i+1)
		}
	}
	if ot.promptUser == nil {
		return "", fmt.Errorf("ask_user is not available: no prompt handler configured")
	}

	requestID := fmt.Sprintf("ask-%d", time.Now().UnixNano())
	return ot.promptUser(ctx, requestID, questions)
}

// operatorToolsToProviderTools converts operator tool definitions to provider.Tool format.
func operatorToolsToProviderTools(defs []runtime.ToolDef) []provider.Tool {
	tools := make([]provider.Tool, len(defs))
	for i, td := range defs {
		tools[i] = provider.Tool{
			Name:        td.Name,
			Description: td.Description,
			Parameters:  td.Parameters,
		}
	}
	return tools
}
