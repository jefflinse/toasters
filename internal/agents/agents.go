// Package agents provides types and functions for loading and managing agent
// definitions from Markdown files with YAML frontmatter.
package agents

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jefflinse/toasters/internal/agentfmt"
	"gopkg.in/yaml.v3"
)

// spawnAgentTaskInstruction is the canonical instruction for populating the
// "task" field when calling spawn_agent. It is referenced verbatim in
// BuildTeamCoordinatorPrompt so the two stay in sync.
const spawnAgentTaskInstruction = `When calling spawn_agent, always populate the "task" field with a short (≤60 char) description of what the worker is being asked to do. This is shown in the TUI card so the operator can monitor progress at a glance. Example: "building core data models", "performing code review", "writing unit tests".`

// Agent represents a single agent loaded from a Markdown file.
type Agent struct {
	// Existing fields (kept for backwards compatibility).
	Name          string          // filename stem (e.g. "prototyper" from "prototyper.md")
	Description   string          // from frontmatter "description" field
	Mode          string          // from frontmatter "mode" field ("primary" = coordinator, anything else = worker)
	Color         string          // from frontmatter "color" field (hex color, e.g. "#FF9800")
	Temperature   float64         // from frontmatter "temperature" field (0 if absent)
	Body          string          // the system prompt text (everything after the closing --- of frontmatter)
	Tools         map[string]bool // tool enable/disable map from agent frontmatter (populated by agentfmt)
	HasToolsBlock bool            // true if allowed or disallowed tools were specified

	// Superset fields from agentfmt.
	Skills          []string       // skill references for composition
	TopP            *float64       // optional top-p sampling parameter
	MaxTurns        int            // maximum conversation turns
	Provider        string         // LLM provider name
	Model           string         // LLM model name
	ModelOptions    map[string]any // provider-specific model options
	AllowedTools    []string       // tool allowlist (from agentfmt Tools)
	DisallowedTools []string       // tool denylist
	PermissionMode  string         // permission mode (e.g. "plan", "acceptEdits")
	Permissions     map[string]any // granular permission config
	MCPServers      any            // MCP server config (list or map)
	Memory          string         // persistent memory/instructions
	Hidden          bool           // hide from UI
	Disabled        bool           // disable agent
	Hooks           map[string]any // lifecycle hooks
	Background      bool           // run in background
	Isolation       string         // isolation mode (e.g. "container")
}

// Registry holds a set of agents split into a coordinator and workers.
type Registry struct {
	Coordinator *Agent  // nil if none found
	Workers     []Agent // all non-coordinator agents
}

// ParseFile reads the Markdown file at path and returns an Agent.
//
// The file is parsed using agentfmt, which supports YAML frontmatter in
// toasters, Claude Code, and OpenCode formats. If the file has no valid
// frontmatter, the entire content becomes the body.
//
// Legacy files with tools as a map (e.g. "bash: false") are handled via
// a fallback that parses the raw YAML and converts the map to the Agent's
// legacy Tools field.
//
// Only file read failures are returned as errors; files without frontmatter
// are treated as body-only agents.
func ParseFile(path string) (Agent, error) {
	stem := filenameStem(path)

	defType, def, err := agentfmt.ParseFile(path)
	if err != nil {
		// agentfmt failed — try legacy tools-map fallback before giving up.
		agent, fallbackErr := parseLegacyFallback(path, stem)
		if fallbackErr == nil {
			return agent, nil
		}
		// Both failed — treat entire content as body.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return Agent{}, fmt.Errorf("reading agent file %s: %w", path, readErr)
		}
		return Agent{Name: stem, Body: strings.TrimSpace(string(data))}, nil
	}

	switch defType {
	case agentfmt.DefAgent:
		return agentDefToAgent(def.(*agentfmt.AgentDef), stem), nil
	case agentfmt.DefSkill:
		skill := def.(*agentfmt.SkillDef)
		return Agent{
			Name:        skill.Name,
			Description: skill.Description,
			Body:        skill.Body,
		}, nil
	case agentfmt.DefTeam:
		team := def.(*agentfmt.TeamDef)
		return Agent{
			Name:        team.Name,
			Description: team.Description,
			Body:        team.Body,
		}, nil
	default:
		return Agent{Name: stem}, nil
	}
}

// agentDefToAgent converts an agentfmt.AgentDef to an Agent, mapping all
// superset fields and maintaining backwards compatibility with the legacy
// Tools map and HasToolsBlock flag.
func agentDefToAgent(def *agentfmt.AgentDef, defaultName string) Agent {
	name := def.Name
	if name == "" {
		name = defaultName
	}

	var temp float64
	if def.Temperature != nil {
		temp = *def.Temperature
	}

	// Build legacy Tools map from allowed/disallowed tool lists.
	var tools map[string]bool
	hasToolsBlock := len(def.Tools) > 0 || len(def.DisallowedTools) > 0
	if hasToolsBlock {
		tools = make(map[string]bool)
		for _, t := range def.Tools {
			tools[t] = true
		}
		for _, t := range def.DisallowedTools {
			tools[t] = false
		}
	}

	return Agent{
		// Existing fields.
		Name:          name,
		Description:   def.Description,
		Mode:          def.Mode,
		Color:         def.Color,
		Temperature:   temp,
		Body:          def.Body,
		Tools:         tools,
		HasToolsBlock: hasToolsBlock,

		// Superset fields.
		Skills:          def.Skills,
		TopP:            def.TopP,
		MaxTurns:        def.MaxTurns,
		Provider:        def.Provider,
		Model:           def.Model,
		ModelOptions:    def.ModelOptions,
		AllowedTools:    def.Tools,
		DisallowedTools: def.DisallowedTools,
		PermissionMode:  def.PermissionMode,
		Permissions:     def.Permissions,
		MCPServers:      def.MCPServers,
		Memory:          def.Memory,
		Hidden:          def.Hidden,
		Disabled:        def.Disabled,
		Hooks:           def.Hooks,
		Background:      def.Background,
		Isolation:       def.Isolation,
	}
}

// filenameStem returns the filename without extension.
func filenameStem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// parseLegacyFallback handles agent files where the tools field is a YAML map
// (e.g. "bash: false") rather than a list. This format was used by the old
// line-by-line parser and is not supported by agentfmt's typed unmarshaling.
func parseLegacyFallback(path, stem string) (Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Agent{}, err
	}

	fmYAML, body, err := agentfmt.SplitFrontmatter(string(data))
	if err != nil {
		return Agent{}, err
	}

	// Parse into a generic map to extract fields including map-style tools.
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &raw); err != nil {
		return Agent{}, err
	}

	agent := Agent{
		Name: stem,
		Body: strings.TrimSpace(body),
	}

	if v, ok := raw["name"].(string); ok && v != "" {
		agent.Name = v
	}
	if v, ok := raw["description"].(string); ok {
		agent.Description = v
	}
	if v, ok := raw["mode"].(string); ok {
		agent.Mode = v
	}
	if v, ok := raw["color"].(string); ok {
		agent.Color = agentfmt.NormalizeColor(v)
	}
	if v, ok := raw["temperature"].(float64); ok {
		agent.Temperature = v
	} else if v, ok := raw["temperature"].(int); ok {
		agent.Temperature = float64(v)
	}

	// Handle tools as a map (legacy format: "bash: false").
	if toolsRaw, ok := raw["tools"]; ok {
		if toolsMap, ok := toolsRaw.(map[string]any); ok {
			agent.HasToolsBlock = true
			agent.Tools = make(map[string]bool, len(toolsMap))
			for k, v := range toolsMap {
				switch bv := v.(type) {
				case bool:
					agent.Tools[k] = bv
				default:
					agent.Tools[k] = true
				}
			}
		}
	}

	return agent, nil
}

// Discover loads all agent Markdown files from dir.
//
// If dir does not exist, an empty slice and nil error are returned. Files that
// fail to parse are skipped with a log warning.
func Discover(dir string) ([]Agent, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return []Agent{}, nil
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("globbing agent files in %s: %w", dir, err)
	}

	agents := make([]Agent, 0, len(matches))
	for _, path := range matches {
		a, err := ParseFile(path)
		if err != nil {
			slog.Warn("skipping agent file", "path", path, "error", err)
			continue
		}
		agents = append(agents, a)
	}

	return agents, nil
}

// BuildRegistry partitions discovered agents into a coordinator and workers.
//
// If coordinatorName is non-empty, the agent whose Name matches
// (case-insensitive) is used as the coordinator. Otherwise, the first agent
// with Mode == "primary" is chosen. All remaining agents become workers.
// If no coordinator is identified, Coordinator is nil and all agents are workers.
func BuildRegistry(discovered []Agent, coordinatorName string) Registry {
	if len(discovered) == 0 {
		return Registry{}
	}

	coordIdx := -1

	if coordinatorName != "" {
		needle := strings.ToLower(coordinatorName)
		for i, a := range discovered {
			if strings.ToLower(a.Name) == needle {
				coordIdx = i
				break
			}
		}
	} else {
		for i, a := range discovered {
			if a.Mode == "primary" {
				coordIdx = i
				break
			}
		}
	}

	if coordIdx < 0 {
		return Registry{Workers: slices.Clone(discovered)}
	}

	coord := discovered[coordIdx]
	workers := make([]Agent, 0, len(discovered)-1)
	for i, a := range discovered {
		if i != coordIdx {
			workers = append(workers, a)
		}
	}

	return Registry{
		Coordinator: &coord,
		Workers:     workers,
	}
}

// Watch monitors dir for Markdown file changes and calls onChange each time a
// change is detected. It blocks until ctx is cancelled. If dir does not exist
// the watcher is started anyway so it picks up the directory once created.
//
// onChange is debounced with a 200ms window to coalesce the multiple fsnotify
// events that editors typically emit per save (write + chmod + rename).
func Watch(ctx context.Context, dir string, onChange func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	defer func() { _ = w.Close() }()

	// Best-effort: ignore error if dir doesn't exist yet.
	_ = w.Add(dir)

	// A nil channel blocks forever, so debounceTimer starts inactive.
	// When set to time.After(...), the case arm becomes selectable.
	// onChange runs on this goroutine — never concurrently with itself.
	var debounceTimer <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-w.Events:
			if !ok {
				return nil
			}
			if strings.HasSuffix(event.Name, ".md") {
				debounceTimer = time.After(200 * time.Millisecond)
			}
		case <-debounceTimer:
			debounceTimer = nil
			onChange()
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			slog.Error("agents watcher error", "error", err)
		}
	}
}

// WatchRecursive watches dir and all immediate subdirectories for .md file
// changes, calling onChange on any event. New subdirectories are added to the
// watch automatically. Blocks until ctx is cancelled.
//
// onChange is debounced with a 200ms window to coalesce the multiple fsnotify
// events that editors typically emit per save (write + chmod + rename).
func WatchRecursive(ctx context.Context, dir string, onChange func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating watcher: %w", err)
	}
	defer func() { _ = w.Close() }()

	// Watch the top-level dir.
	_ = w.Add(dir) // best-effort

	// Also watch all existing subdirectories.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			_ = w.Add(filepath.Join(dir, e.Name()))
		}
	}

	// A nil channel blocks forever, so debounceTimer starts inactive.
	// When set to time.After(...), the case arm becomes selectable.
	// onChange runs on this goroutine — never concurrently with itself.
	var debounceTimer <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-w.Events:
			if !ok {
				return nil
			}
			// If a new directory was created, start watching it.
			if event.Op&fsnotify.Create != 0 {
				info, err := os.Stat(event.Name)
				if err == nil && info.IsDir() {
					_ = w.Add(event.Name)
				}
			}
			// Debounce onChange for any .md file event.
			if strings.HasSuffix(event.Name, ".md") {
				debounceTimer = time.After(200 * time.Millisecond)
			}
		case <-debounceTimer:
			debounceTimer = nil
			onChange()
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			slog.Error("jobs watcher error", "error", err)
		}
	}
}

// Team represents a named group of agents loaded from a subdirectory.
// The coordinator is the agent with mode=="primary"; all others are workers.
type Team struct {
	Name        string  // directory name (e.g. "coding")
	Description string  // from team.md frontmatter (empty if no team.md)
	Dir         string  // absolute path to team directory
	Coordinator *Agent  // nil if no primary agent found
	Workers     []Agent // all non-coordinator agents
}

// DiscoverTeams loads all teams from subdirectories of teamsDir.
//
// Each subdirectory (excluding hidden dirs starting with ".") is treated as a
// team. Discover is called on the subdir, then BuildRegistry splits the agents
// into coordinator and workers. Subdirs with no .md files are included as empty teams.
// If teamsDir does not exist, an empty slice and nil error are returned.
func DiscoverTeams(teamsDir string) ([]Team, error) {
	if _, err := os.Stat(teamsDir); os.IsNotExist(err) {
		return []Team{}, nil
	}

	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return nil, fmt.Errorf("reading teams directory %s: %w", teamsDir, err)
	}

	var teams []Team
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		subdir := filepath.Join(teamsDir, entry.Name())
		discovered, err := Discover(filepath.Join(subdir, "agents"))
		if err != nil {
			slog.Warn("skipping team directory", "path", subdir, "error", err)
			continue
		}

		reg := BuildRegistry(discovered, "")

		team := Team{
			Name:        entry.Name(),
			Dir:         subdir,
			Coordinator: reg.Coordinator,
			Workers:     reg.Workers,
		}

		// Check for team.md metadata.
		teamMDPath := filepath.Join(subdir, "team.md")
		if teamDef, err := agentfmt.ParseTeam(teamMDPath); err == nil {
			team.Description = teamDef.Description
			if teamDef.Lead != "" {
				// Override coordinator selection based on team.md lead field.
				reg = BuildRegistry(discovered, teamDef.Lead)
				team.Coordinator = reg.Coordinator
				team.Workers = reg.Workers
			}
		}

		teams = append(teams, team)
	}

	return teams, nil
}

// BuildTeamCoordinatorPrompt returns the full system prompt for a team coordinator
// Claude subprocess. If team.Coordinator is nil, only the instructions block is
// returned (no coordinator body prepended).
// jobDir is the absolute path to the job's workspace directory.
func BuildTeamCoordinatorPrompt(team Team, jobDir string) string {
	var sb strings.Builder

	// Prepend coordinator body if present.
	if team.Coordinator != nil && team.Coordinator.Body != "" {
		sb.WriteString(team.Coordinator.Body)
		sb.WriteString("\n\n---\n\n")
	}

	// Build worker roster.
	var roster strings.Builder
	if len(team.Workers) == 0 {
		roster.WriteString("(no workers configured)")
	} else {
		for _, w := range team.Workers {
			fmt.Fprintf(&roster, "- `%s`: %s\n", w.Name, w.Description)
		}
	}

	fmt.Fprintf(&sb, `## Toasters Team Coordinator Instructions

You are the coordinator for the "%s" team inside toasters, an agentic orchestration tool.

Your job is to take the assigned job and task, plan the work, delegate to your team members using the Task tool, and drive the job to completion autonomously.

`+spawnAgentTaskInstruction+`

### Your Team
%s

### Completing Work
When the job is complete, write a REPORT.md file to the job directory with this exact format:

`+"```"+`markdown
---
team: %s
status: complete
summary: One paragraph describing what was accomplished.
artifacts: []
---

## What Was Done
...

## Key Decisions Made
...

## Remaining Work
None.
`+"```"+`

### Escalating Blockers
If you encounter a genuine blocker that cannot be resolved autonomously — something that requires a human decision — write a BLOCKER.md file to the job directory with this format:

`+"```"+`markdown
---
team: %s
blocker: Short one-line description of what is blocking
---

## Context
...

## What Was Tried
...

## What Is Needed From User
...
`+"```"+`

Then stop work and exit. The operator will surface this to the user and resume the job once resolved.

Do not ask for confirmation before starting work. Do not ask for approval of your plan. Work autonomously and escalate only genuine blockers.`,
		team.Name,
		strings.TrimRight(roster.String(), "\n"),
		team.Name,
		team.Name,
	)

	fmt.Fprintf(&sb, `

### Job Directory
Your job directory is: %s

All work artifacts (code, cloned repositories, generated files, etc.) must be written under this directory.
Clone repositories to: %s/repos/<owner>/<repo>/
Write REPORT.md to: %s/REPORT.md
Write BLOCKER.md to: %s/BLOCKER.md`, jobDir, jobDir, jobDir, jobDir)

	return sb.String()
}

// SetCoordinator updates a team so that exactly one agent — the one whose
// frontmatter name field matches agentName (case-insensitive) — is the
// coordinator. It does two things:
//
//  1. Rewrites team.md's lead: field to agentName so that DiscoverTeams picks
//     up the change immediately (lead: takes precedence over mode: in agent files).
//  2. Rewrites all agent .md files in teamDir/agents/ so that the target agent
//     has mode: primary and all others have mode: worker.
//
// Partial updates are acceptable on write failure (prototype behaviour).
func SetCoordinator(teamDir, agentName string) error {
	agentsDir := filepath.Join(teamDir, "agents")
	matches, err := filepath.Glob(filepath.Join(agentsDir, "*.md"))
	if err != nil {
		return fmt.Errorf("globbing agent files in %s: %w", agentsDir, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no agent files found in %s", agentsDir)
	}

	// Parse each agent file to get its frontmatter name, then match
	// case-insensitively against agentName. This is consistent with how
	// BuildRegistry resolves the lead: field from team.md.
	needle := strings.ToLower(agentName)
	type agentFile struct {
		path string
		name string // frontmatter name (falls back to filename stem)
	}
	var agentFiles []agentFile
	for _, p := range matches {
		stem := strings.TrimSuffix(filepath.Base(p), ".md")
		name := stem // default: filename stem
		if a, parseErr := ParseFile(p); parseErr == nil && a.Name != "" {
			name = a.Name
		}
		agentFiles = append(agentFiles, agentFile{path: p, name: name})
	}

	// Verify the target agent exists.
	found := false
	for _, af := range agentFiles {
		if strings.ToLower(af.name) == needle {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("agent %q not found in %s", agentName, agentsDir)
	}

	// Update team.md's lead: field so DiscoverTeams picks up the change.
	// lead: takes precedence over mode: in agent files, so this is the
	// authoritative way to set the coordinator.
	teamMDPath := filepath.Join(teamDir, "team.md")
	if teamDef, parseErr := agentfmt.ParseTeam(teamMDPath); parseErr == nil {
		teamDef.Lead = agentName
		if writeErr := writeTeamFileTo(teamMDPath, teamDef); writeErr != nil {
			return fmt.Errorf("updating team.md lead: %w", writeErr)
		}
	}
	// If team.md doesn't exist or can't be parsed, fall through to mode rewriting
	// as a best-effort fallback.

	// Rewrite mode: in each agent file.
	for _, af := range agentFiles {
		targetMode := "worker"
		if strings.ToLower(af.name) == needle {
			targetMode = "primary"
		}

		data, err := os.ReadFile(af.path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", af.path, err)
		}

		newContent := rewriteMode(string(data), targetMode)

		tmp, err := os.CreateTemp(agentsDir, "agent-*.md.tmp")
		if err != nil {
			return fmt.Errorf("creating temp file in %s: %w", agentsDir, err)
		}
		tmpName := tmp.Name()

		if _, err := tmp.WriteString(newContent); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return fmt.Errorf("writing temp file %s: %w", tmpName, err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("closing temp file %s: %w", tmpName, err)
		}
		if err := os.Rename(tmpName, af.path); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("renaming %s to %s: %w", tmpName, af.path, err)
		}
	}

	return nil
}

// writeTeamFileTo writes a TeamDef as a toasters-format .md file. It is the
// agents-package equivalent of the TUI's writeTeamFile helper, used by
// SetCoordinator to update team.md without importing the tui package.
func writeTeamFileTo(path string, def *agentfmt.TeamDef) error {
	data, err := yaml.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshaling team frontmatter: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	// Trim trailing newline that yaml.Marshal appends, then add our own.
	sb.WriteString(strings.TrimRight(string(data), "\n"))
	sb.WriteString("\n---\n")
	if def.Body != "" {
		sb.WriteString(def.Body)
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// rewriteMode returns content with the frontmatter mode: field set to mode.
// It handles three cases:
//  1. File has frontmatter with an existing mode: line — replace it.
//  2. File has frontmatter but no mode: line — insert before closing ---.
//  3. File has no frontmatter — prepend a minimal frontmatter block.
func rewriteMode(content, mode string) string {
	const delim = "---"
	modeLine := "mode: " + mode

	if !strings.HasPrefix(content, delim+"\n") {
		// No frontmatter — prepend a minimal block.
		return delim + "\n" + modeLine + "\n" + delim + "\n" + content
	}

	// Split into opening delimiter, frontmatter body, and the rest.
	// content starts with "---\n"; find the closing "\n---".
	rest := content[len(delim)+1:] // strip leading "---\n"
	closingIdx := strings.Index(rest, "\n"+delim)
	if closingIdx < 0 {
		// Malformed — no closing delimiter; prepend a block anyway.
		return delim + "\n" + modeLine + "\n" + delim + "\n" + content
	}

	fmBlock := rest[:closingIdx]                 // lines between the two ---
	afterClose := rest[closingIdx+1+len(delim):] // everything after closing ---

	lines := strings.Split(fmBlock, "\n")
	modeFound := false
	for i, line := range lines {
		if strings.HasPrefix(line, "mode:") {
			lines[i] = modeLine
			modeFound = true
			break
		}
	}
	if !modeFound {
		// Insert mode: just before the closing ---.
		lines = append(lines, modeLine)
	}

	var sb strings.Builder
	sb.WriteString(delim + "\n")
	sb.WriteString(strings.Join(lines, "\n"))
	sb.WriteString("\n" + delim)
	sb.WriteString(afterClose)
	return sb.String()
}
