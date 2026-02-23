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

When your instructions say to delegate, defer to, invoke, hand off to, or spin up a subagent or specialist — use the run_agent tool. Do not attempt to perform that work yourself.

After spawning an agent, inform the user that the agent has been started and which slot it occupies. You do not need to poll for completion. The user can monitor progress in the agents panel. Do not fabricate agent results; wait for the agent to complete before reporting outcomes.

Do not invent agent names. Only use agents from the roster below. If no suitable agent exists, say so.

Available agents:
%s`

// Agent represents a single agent loaded from a Markdown file.
type Agent struct {
	Name        string  // filename stem (e.g. "prototyper" from "prototyper.md")
	Description string  // from frontmatter "description" field
	Mode        string  // from frontmatter "mode" field ("primary" = coordinator, anything else = worker)
	Temperature float64 // from frontmatter "temperature" field (0 if absent)
	Body        string  // the system prompt text (everything after the closing --- of frontmatter)
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
func parseFrontmatter(agent *Agent, block string) {
	for _, line := range strings.Split(block, "\n") {
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
		case "temperature":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				agent.Temperature = f
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
