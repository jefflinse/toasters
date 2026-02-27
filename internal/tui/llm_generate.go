// LLM generation helpers: async tea.Cmd factories for generating skill, agent,
// and team definitions via an LLM provider.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

const llmGenerateTimeout = 30 * time.Second

// generateSkillCmd returns a tea.Cmd that asks the LLM to generate a Toasters
// skill definition file for the given user prompt. The result is delivered as
// a skillGeneratedMsg.
func generateSkillCmd(client provider.Provider, prompt string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return skillGeneratedMsg{err: fmt.Errorf("no LLM provider configured")}
		}

		systemPrompt := `You are generating a Toasters skill definition file. Output ONLY the raw .md file content with no explanation, preamble, or code fences.

A skill file has this format:
---
name: skill-name
description: Brief description of what this skill provides
tools:
  - tool_name_1
  - tool_name_2
---

# Skill Name

Detailed instructions for the agent using this skill. This is the system prompt content that will be injected when this skill is active.

## Guidelines
- ...`

		userMsg := fmt.Sprintf("The user wants a skill for: %s\n\nOutput ONLY the .md file content starting with ---.", prompt)

		msgs := []provider.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		}

		ctx, cancel := context.WithTimeout(context.Background(), llmGenerateTimeout)
		defer cancel()

		content, err := provider.ChatCompletion(ctx, client, msgs)
		if err != nil {
			return skillGeneratedMsg{err: fmt.Errorf("LLM call failed: %w", err)}
		}

		content = stripCodeFences(content)

		if _, err := agentfmt.ParseBytes([]byte(content), agentfmt.DefSkill); err != nil {
			return skillGeneratedMsg{err: fmt.Errorf("generated content is not a valid skill definition: %w", err)}
		}

		return skillGeneratedMsg{content: content}
	}
}

// generateAgentCmd returns a tea.Cmd that asks the LLM to generate a Toasters
// agent definition file for the given user prompt. The result is delivered as
// an agentGeneratedMsg.
func generateAgentCmd(client provider.Provider, prompt string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return agentGeneratedMsg{err: fmt.Errorf("no LLM provider configured")}
		}

		systemPrompt := `You are generating a Toasters agent definition file. Output ONLY the raw .md file content with no explanation, preamble, or code fences.

A Toasters agent file has this format:
---
name: agent-name
description: What this agent does
mode: worker
model: claude-sonnet-4-5
skills:
  - skill-name
tools:
  - Read
  - Write
  - Bash
---

# Agent Name

Detailed system prompt for this agent. Describe its persona, responsibilities, and how it should behave.`

		userMsg := fmt.Sprintf("The user wants an agent for: %s\n\nOutput ONLY the .md file content starting with ---.", prompt)

		msgs := []provider.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		}

		ctx, cancel := context.WithTimeout(context.Background(), llmGenerateTimeout)
		defer cancel()

		content, err := provider.ChatCompletion(ctx, client, msgs)
		if err != nil {
			return agentGeneratedMsg{err: fmt.Errorf("LLM call failed: %w", err)}
		}

		content = stripCodeFences(content)

		if _, err := agentfmt.ParseBytes([]byte(content), agentfmt.DefAgent); err != nil {
			return agentGeneratedMsg{err: fmt.Errorf("generated content is not a valid agent definition: %w", err)}
		}

		return agentGeneratedMsg{content: content}
	}
}

// generateTeamCmd returns a tea.Cmd that asks the LLM to generate a Toasters
// team definition for the given user prompt, selecting from availableAgents.
// The result is delivered as a teamGeneratedMsg.
func generateTeamCmd(client provider.Provider, prompt string, availableAgents []*db.Agent) tea.Cmd {
	// Capture a copy of the agent list for the goroutine.
	agentsCopy := make([]*db.Agent, len(availableAgents))
	copy(agentsCopy, availableAgents)

	return func() tea.Msg {
		if client == nil {
			return teamGeneratedMsg{err: fmt.Errorf("no LLM provider configured")}
		}

		// Build the agent list section for the system prompt.
		var agentList strings.Builder
		for _, a := range agentsCopy {
			desc := a.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&agentList, "- %s: %s\n", a.Name, desc)
		}

		systemPrompt := fmt.Sprintf(`You are generating a Toasters team definition. Output ONLY a JSON object with no explanation, preamble, or code fences.

A team.md file has this format:
---
name: team-name
description: What this team does
coordinator: lead-agent-name
---

# Team Name

Team culture and working norms. How agents on this team should collaborate.

Available agents that can be assigned to this team:
%s
Output ONLY a JSON object in this exact format:
{"team_md": "<the full team.md content>", "agent_names": ["agent1", "agent2"]}

The agent_names must be names from the available agents list above. Choose 2-5 agents that best fit the team's purpose.`, agentList.String())

		userMsg := fmt.Sprintf("The user wants a team for: %s", prompt)

		msgs := []provider.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		}

		ctx, cancel := context.WithTimeout(context.Background(), llmGenerateTimeout)
		defer cancel()

		raw, err := provider.ChatCompletion(ctx, client, msgs)
		if err != nil {
			return teamGeneratedMsg{err: fmt.Errorf("LLM call failed: %w", err)}
		}

		raw = stripCodeFences(raw)

		var result struct {
			TeamMD     string   `json:"team_md"`
			AgentNames []string `json:"agent_names"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return teamGeneratedMsg{err: fmt.Errorf("parsing LLM JSON response: %w", err)}
		}

		if result.TeamMD == "" {
			return teamGeneratedMsg{err: fmt.Errorf("LLM returned empty team_md")}
		}

		if _, err := agentfmt.ParseBytes([]byte(result.TeamMD), agentfmt.DefTeam); err != nil {
			return teamGeneratedMsg{err: fmt.Errorf("generated team_md is not a valid team definition: %w", err)}
		}

		return teamGeneratedMsg{
			content:    result.TeamMD,
			agentNames: result.AgentNames,
		}
	}
}

// stripCodeFences removes markdown code fences from LLM output. Some models
// wrap their output in ```yaml or ``` blocks despite being instructed not to.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)

	// Strip opening fence (``` or ```yaml, ```json, etc.).
	if strings.HasPrefix(s, "```") {
		// Find the end of the first line.
		idx := strings.Index(s, "\n")
		if idx != -1 {
			s = s[idx+1:]
		}
	}

	// Strip closing fence.
	if strings.HasSuffix(s, "```") {
		s = s[:len(s)-3]
	}

	return strings.TrimSpace(s)
}
