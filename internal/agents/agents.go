// Package agents provides types and functions for loading and managing agent
// definitions from Markdown files with optional YAML-like frontmatter.
package agents

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// WrapperPrompt is the toasters-owned framing text appended to every coordinator
// system prompt. The %s placeholder is replaced with the agent roster at runtime.
const WrapperPrompt = `You are operating inside toasters, a TUI orchestration tool for agentic coding work.

You have access to a run_agent tool that spawns a claude CLI subprocess in a background slot. Up to 4 slots can run concurrently. Each slot runs independently and streams its output back to the TUI.

When your instructions say to delegate, defer to, invoke, hand off to, or spin up a subagent or specialist — use the run_agent tool. Do not attempt to perform that work yourself. Spawn the agent immediately without asking the user for confirmation first.

When an agent completes, you will automatically receive its output as a new message. Review the output and follow up accordingly — summarize what was done, flag any issues, or take the next step.

Do not invent agent names. Only use agents from the roster below. If no suitable agent exists, say so.

Available agents:
%s`

// Agent represents a single agent loaded from a Markdown file.
type Agent struct {
	Name          string          // filename stem (e.g. "prototyper" from "prototyper.md")
	Description   string          // from frontmatter "description" field
	Mode          string          // from frontmatter "mode" field ("primary" = coordinator, anything else = worker)
	Color         string          // from frontmatter "color" field (hex color, e.g. "#FF9800")
	Temperature   float64         // from frontmatter "temperature" field (0 if absent)
	Body          string          // the system prompt text (everything after the closing --- of frontmatter)
	Tools         map[string]bool // from frontmatter "tools:" block; key=tool name, value=allowed (false=denied)
	HasToolsBlock bool            // true if a "tools:" block was present in frontmatter
}

// ClaudePermissionArgs returns the Claude CLI permission flags for this agent.
//
// If the agent has a tools: block in its frontmatter, the denied tools are
// translated to a --allowedTools allow-list (full set minus denied tools).
// If no tools: block is present, --dangerously-skip-permissions is used
// (full access — appropriate for agents like builder that need everything).
//
// Note: when --allowedTools is used, the prompt MUST be passed via stdin
// rather than as a positional argument, as the flag greedily consumes
// subsequent positional args as tool names. The gateway handles this.
func (a Agent) ClaudePermissionArgs() []string {
	if !a.HasToolsBlock {
		return []string{"--dangerously-skip-permissions"}
	}

	// Full set of Claude Code built-in tools.
	fullSet := []string{"Bash", "Read", "Write", "Edit", "Glob", "Grep", "WebFetch", "TodoRead", "TodoWrite"}

	// OpenCode tools: key → Claude Code tool name.
	openCodeToClaudeCode := map[string]string{
		"bash":  "Bash",
		"write": "Write",
		"edit":  "Edit",
	}

	denied := map[string]bool{}
	for ocKey, allowed := range a.Tools {
		if !allowed {
			if ccName, ok := openCodeToClaudeCode[ocKey]; ok {
				denied[ccName] = true
			}
		}
	}

	var allowed []string
	for _, tool := range fullSet {
		if !denied[tool] {
			allowed = append(allowed, tool)
		}
	}

	if len(allowed) == 0 {
		return []string{"--permission-mode", "bypassPermissions"}
	}
	// --permission-mode acceptEdits prevents the interactive plan-approval prompt
	// while still respecting the --allowedTools constraint.
	return []string{"--permission-mode", "acceptEdits", "--allowedTools", strings.Join(allowed, ",")}
}

// Registry holds a set of agents split into a coordinator and workers.
type Registry struct {
	Coordinator *Agent  // nil if none found
	Workers     []Agent // all non-coordinator agents
}

// ParseFile reads the Markdown file at path and returns an Agent.
//
// If the file begins with "---\n", the content up to the next "\n---\n" (or
// "\n---" at EOF) is treated as frontmatter and parsed line-by-line for
// key: value pairs. The remainder is the body. If no frontmatter delimiter is
// present, the entire file content becomes the body.
//
// Only file read failures are returned as errors; malformed frontmatter lines
// are silently skipped.
func ParseFile(path string) (Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Agent{}, fmt.Errorf("reading agent file %s: %w", path, err)
	}

	stem := filepath.Base(path)
	stem = strings.TrimSuffix(stem, filepath.Ext(stem))

	agent := Agent{Name: stem}

	content := string(data)

	const fmDelim = "---"
	if strings.HasPrefix(content, fmDelim+"\n") {
		// Strip the opening "---\n"
		rest := content[len(fmDelim)+1:]

		// Find the closing "\n---" (may be followed by "\n" or EOF)
		closingIdx := strings.Index(rest, "\n"+fmDelim)
		if closingIdx >= 0 {
			fmBlock := rest[:closingIdx]
			// Advance past "\n---"; skip an optional trailing newline
			afterClose := rest[closingIdx+1+len(fmDelim):]
			if strings.HasPrefix(afterClose, "\n") {
				afterClose = afterClose[1:]
			}
			agent.Body = strings.TrimSpace(afterClose)
			parseFrontmatter(&agent, fmBlock)
		} else {
			// No closing delimiter — treat everything as body
			agent.Body = strings.TrimSpace(content)
		}
	} else {
		agent.Body = strings.TrimSpace(content)
	}

	return agent, nil
}

// parseFrontmatter scans the frontmatter block line by line and populates
// the known fields on agent. Lines that don't match "key: value" are ignored.
// Multi-line blocks (e.g. "tools:") are handled by entering a block-parsing
// mode when a line has no value after the colon.
func parseFrontmatter(agent *Agent, block string) {
	inToolsBlock := false

	for _, line := range strings.Split(block, "\n") {
		// A line starting with whitespace may be a tools block entry.
		if inToolsBlock {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || !strings.HasPrefix(line, " ") {
				// Blank line or non-indented line exits the tools block.
				inToolsBlock = false
				// Fall through to process this line as a top-level key.
			} else {
				// Parse "  key: value" tool entry.
				idx := strings.Index(trimmed, ":")
				if idx >= 0 {
					toolKey := strings.TrimSpace(trimmed[:idx])
					toolVal := strings.TrimSpace(trimmed[idx+1:])
					agent.Tools[toolKey] = toolVal != "false"
				}
				continue
			}
		}

		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		// Strip surrounding double-quotes if present
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}

		switch key {
		case "description":
			agent.Description = val
		case "mode":
			agent.Mode = val
		case "color":
			agent.Color = val
		case "temperature":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				agent.Temperature = f
			}
		case "tools":
			if val == "" {
				// Multi-line tools block — enter block mode.
				agent.HasToolsBlock = true
				agent.Tools = make(map[string]bool)
				inToolsBlock = true
			}
		}
	}
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
			log.Printf("warning: skipping agent file %s: %v", path, err)
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
		workers := make([]Agent, len(discovered))
		copy(workers, discovered)
		return Registry{Workers: workers}
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
func Watch(ctx context.Context, dir string, onChange func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	defer w.Close()

	// Best-effort: ignore error if dir doesn't exist yet.
	_ = w.Add(dir)

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-w.Events:
			if !ok {
				return nil
			}
			if strings.HasSuffix(event.Name, ".md") {
				onChange()
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("agents watcher error: %v", err)
		}
	}
}

// Team represents a named group of agents loaded from a subdirectory.
// The coordinator is the agent with mode=="primary"; all others are workers.
type Team struct {
	Name        string  // directory name (e.g. "coding")
	Dir         string  // absolute path to team directory
	Coordinator *Agent  // nil if no primary agent found
	Workers     []Agent // all non-coordinator agents
}

// BuildSystemPrompt assembles the full system prompt for the coordinator by
// appending the toasters wrapper (with agent roster) to the coordinator's body.
//
// Workers with an empty description are omitted from the roster. If no workers
// are present, the roster section reads "No worker agents discovered."
func BuildSystemPrompt(coordinator Agent, workers []Agent) string {
	var rosterLines []string
	for _, w := range workers {
		if w.Description == "" {
			continue
		}
		rosterLines = append(rosterLines, fmt.Sprintf("- `%s`: %s", w.Name, w.Description))
	}

	var roster string
	if len(rosterLines) == 0 {
		roster = "No worker agents discovered."
	} else {
		roster = strings.Join(rosterLines, "\n")
	}

	return coordinator.Body + "\n\n---\n\n" + fmt.Sprintf(WrapperPrompt, roster)
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
		discovered, err := Discover(subdir)
		if err != nil {
			log.Printf("warning: skipping team directory %s: %v", subdir, err)
			continue
		}

		reg := BuildRegistry(discovered, "")
		teams = append(teams, Team{
			Name:        entry.Name(),
			Dir:         subdir,
			Coordinator: reg.Coordinator,
			Workers:     reg.Workers,
		})
	}

	return teams, nil
}

// AutoDetectTeams checks well-known agent directories and returns any teams found.
//
// It checks ~/.opencode/agents/ (team name "opencode") and ~/.claude/agents/
// (team name "claude"). Only directories with at least one agent are included.
// Returns an empty (non-nil) slice if neither directory exists or has agents.
func AutoDetectTeams() []Team {
	home, err := os.UserHomeDir()
	if err != nil {
		return []Team{}
	}

	candidates := []struct {
		name string
		dir  string
	}{
		{"opencode", filepath.Join(home, ".opencode", "agents")},
		{"claude", filepath.Join(home, ".claude", "agents")},
	}

	teams := make([]Team, 0)
	for _, c := range candidates {
		discovered, err := Discover(c.dir)
		if err != nil || len(discovered) == 0 {
			continue
		}
		reg := BuildRegistry(discovered, "")
		teams = append(teams, Team{
			Name:        c.name,
			Dir:         c.dir,
			Coordinator: reg.Coordinator,
			Workers:     reg.Workers,
		})
	}

	return teams
}

// BuildTeamCoordinatorPrompt returns the full system prompt for a team coordinator
// Claude subprocess. If team.Coordinator is nil, only the instructions block is
// returned (no coordinator body prepended).
func BuildTeamCoordinatorPrompt(team Team) string {
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
			roster.WriteString(fmt.Sprintf("- `%s`: %s\n", w.Name, w.Description))
		}
	}

	sb.WriteString(fmt.Sprintf(`## Toasters Team Coordinator Instructions

You are the coordinator for the "%s" team inside toasters, an agentic orchestration tool.

Your job is to take the assigned work effort and task, plan the work, delegate to your team members using the Task tool, and drive the effort to completion autonomously.

### Your Team
%s

### Completing Work
When the work effort is complete, write a REPORT.md file to the work effort directory with this exact format:

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
If you encounter a genuine blocker that cannot be resolved autonomously — something that requires a human decision — write a BLOCKER.md file to the work effort directory with this format:

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

Then stop work and exit. The operator will surface this to the user and resume the work effort once resolved.

Do not ask for confirmation before starting work. Do not ask for approval of your plan. Work autonomously and escalate only genuine blockers.`,
		team.Name,
		strings.TrimRight(roster.String(), "\n"),
		team.Name,
		team.Name,
	))

	return sb.String()
}

// SetCoordinator atomically rewrites all .md files in teamDir so that exactly
// one agent — the one whose filename stem matches agentName — has mode: primary.
// All other agents are set to mode: worker. Partial updates are acceptable on
// write failure (prototype behaviour).
func SetCoordinator(teamDir, agentName string) error {
	matches, err := filepath.Glob(filepath.Join(teamDir, "*.md"))
	if err != nil {
		return fmt.Errorf("globbing agent files in %s: %w", teamDir, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no agent files found in %s", teamDir)
	}

	// Verify the target agent exists.
	found := false
	for _, p := range matches {
		stem := strings.TrimSuffix(filepath.Base(p), ".md")
		if stem == agentName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("agent %q not found in %s", agentName, teamDir)
	}

	for _, p := range matches {
		stem := strings.TrimSuffix(filepath.Base(p), ".md")
		targetMode := "worker"
		if stem == agentName {
			targetMode = "primary"
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}

		newContent := rewriteMode(string(data), targetMode)

		tmp, err := os.CreateTemp(teamDir, "agent-*.md.tmp")
		if err != nil {
			return fmt.Errorf("creating temp file in %s: %w", teamDir, err)
		}
		tmpName := tmp.Name()

		if _, err := tmp.WriteString(newContent); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return fmt.Errorf("writing temp file %s: %w", tmpName, err)
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpName)
			return fmt.Errorf("closing temp file %s: %w", tmpName, err)
		}
		if err := os.Rename(tmpName, p); err != nil {
			os.Remove(tmpName)
			return fmt.Errorf("renaming %s to %s: %w", tmpName, p, err)
		}
	}

	return nil
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

// BuildOperatorPrompt returns the hardcoded toasters operator system prompt,
// listing all available teams.
func BuildOperatorPrompt(teams []Team) string {
	var teamList strings.Builder
	if len(teams) == 0 {
		teamList.WriteString("No teams configured. Ask the user to set up a teams directory.")
	} else {
		for _, t := range teams {
			if t.Coordinator != nil {
				teamList.WriteString(fmt.Sprintf("- `%s`: %s\n", t.Name, t.Coordinator.Description))
			} else {
				teamList.WriteString(fmt.Sprintf("- `%s`: %d workers\n", t.Name, len(t.Workers)))
			}
		}
	}

	return fmt.Sprintf(`You are the Operator — the central dispatcher for toasters, an agentic orchestration tool.

Your responsibilities:
- Receive high-level requests from the user
- Create work efforts to track the work
- Assign work efforts to the appropriate team using the assign_team tool
- Surface team blockers and completion summaries to the user
- You do NOT plan, code, review, or do any domain work yourself — that is the teams' job

When a team completes, you will receive its report. Summarize the outcome for the user and suggest next steps if appropriate.

When a team is blocked, you will receive a blocker description. Present it clearly to the user and wait for their input before resuming.

Assign work to teams immediately when requested — do not ask for confirmation or present a plan first.

## Available Teams
%s`, strings.TrimRight(teamList.String(), "\n"))
}
