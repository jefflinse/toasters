package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mdfmt"
	"github.com/jefflinse/toasters/internal/provider"
)

// ListSkills returns all skills from the store, ordered by source then name.
func (s *LocalService) ListSkills(ctx context.Context) ([]Skill, error) {
	if s.cfg.Store == nil {
		return nil, Unavailablef("store not configured")
	}
	dbSkills, err := s.cfg.Store.ListSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}
	skills := make([]Skill, 0, len(dbSkills))
	for _, sk := range dbSkills {
		skills = append(skills, dbSkillToService(sk))
	}
	return skills, nil
}

// GetSkill returns a single skill by ID.
func (s *LocalService) GetSkill(ctx context.Context, id string) (Skill, error) {
	if s.cfg.Store == nil {
		return Skill{}, Unavailablef("store not configured")
	}
	sk, err := s.cfg.Store.GetSkill(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Skill{}, fmt.Errorf("getting skill %s: %w", id, ErrNotFound)
		}
		return Skill{}, fmt.Errorf("getting skill %s: %w", id, err)
	}
	return dbSkillToService(sk), nil
}

// CreateSkill writes a template .md file to the user skills directory and
// triggers a definition reload. Returns the created skill.
func (s *LocalService) CreateSkill(ctx context.Context, name string) (Skill, error) {
	name = sanitizeName(name)
	skillsDir := filepath.Join(s.cfg.ConfigDir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return Skill{}, sanitizeError(fmt.Errorf("creating skills dir: %w", err))
	}

	filename := loader.Slugify(name) + ".md"
	if filename == ".md" {
		return Skill{}, fmt.Errorf("invalid skill name: produces empty filename")
	}
	path := filepath.Join(skillsDir, filename)

	if _, err := os.Stat(path); err == nil {
		return Skill{}, Conflictf("skill file %q already exists", filename)
	}

	template := fmt.Sprintf(`---
name: %s
description: A brief description of what this skill does
tools: []
---

Your skill prompt goes here. This text will be injected into workers that use this skill.
`, name)

	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return Skill{}, sanitizeError(fmt.Errorf("writing skill file: %w", err))
	}

	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after skill creation", "error", err)
		}
	}

	// Find the created skill by name.
	skills, err := s.ListSkills(ctx)
	if err != nil {
		return Skill{}, sanitizeError(fmt.Errorf("listing skills after creation: %w", err))
	}
	for _, sk := range skills {
		if sk.Name == name {
			return sk, nil
		}
	}
	return Skill{}, fmt.Errorf("skill %q not found after creation", name)
}

// DeleteSkill removes the skill's source file and triggers a reload.
func (s *LocalService) DeleteSkill(ctx context.Context, id string) error {
	sk, err := s.GetSkill(ctx, id)
	if err != nil {
		return err
	}
	if sk.Source == "system" {
		return Invalidf("cannot delete system skill %q", sk.Name)
	}
	if sk.SourcePath == "" {
		return Invalidf("skill %q has no source path", sk.Name)
	}
	allowedDir := filepath.Join(s.cfg.ConfigDir, "user")
	realSkillPath, err := filepath.EvalSymlinks(sk.SourcePath)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving skill path: %w", err))
	}
	realAllowedDir, err := filepath.EvalSymlinks(allowedDir)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving allowed dir: %w", err))
	}
	if !strings.HasPrefix(realSkillPath+string(filepath.Separator), realAllowedDir+string(filepath.Separator)) {
		return sanitizeError(Invalidf("skill source path is outside user directory"))
	}
	if err := os.Remove(realSkillPath); err != nil {
		return sanitizeError(fmt.Errorf("removing skill file: %w", err))
	}
	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after skill deletion", "error", err)
		}
	}
	return nil
}

// GenerateSkill asks the LLM to generate a skill definition. Returns an
// operationID immediately; pushes operation.completed or operation.failed when done.
func (s *LocalService) GenerateSkill(ctx context.Context, prompt string) (string, error) {
	if s.currentProvider() == nil {
		return "", Unavailablef("LLM provider not configured")
	}
	if len(prompt) > maxPromptLen {
		return "", fmt.Errorf("prompt too large: %d bytes exceeds maximum %d", len(prompt), maxPromptLen)
	}

	uuidVal, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating operation ID: %w", err)
	}
	operationID := uuidVal.String()

	if !s.tryAcquireAsync() {
		return "", Busyf("too many concurrent operations (max %d)", maxConcurrentOps)
	}

	s.safeGo(operationID, "generate_skill", func() {
		defer s.releaseAsync()

		genCtx, genCancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer genCancel()
		content, genErr := s.generateSkillContent(genCtx, prompt)
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if genErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "generate_skill",
					Error: sanitizeErrorString(genErr),
				},
			})
			return
		}

		path, writeErr := s.writeGeneratedSkillFile(content)
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if writeErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "generate_skill",
					Error: sanitizeErrorString(writeErr),
				},
			})
			return
		}

		if s.cfg.Loader != nil {
			if err := s.cfg.Loader.Load(s.ctx); err != nil {
				slog.Warn("failed to reload definitions after skill generation", "error", err)
			}
		}

		if s.ctx.Err() != nil {
			return // service shutting down
		}
		slog.Info("generated skill file", "path", path)
		s.broadcast(Event{
			Type:        EventTypeOperationCompleted,
			OperationID: operationID,
			Payload: OperationCompletedPayload{
				Kind: "generate_skill",
				Result: OperationResult{
					OperationID: operationID,
					Content:     content,
				},
			},
		})
	})

	return operationID, nil
}

// generateSkillContent calls the LLM to generate a skill definition.
func (s *LocalService) generateSkillContent(ctx context.Context, prompt string) (string, error) {
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

Detailed instructions for the worker using this skill. This is the system prompt content that will be injected when this skill is active.

## Guidelines
- ...`

	userMsg := fmt.Sprintf("The user wants a skill for: %s\n\nOutput ONLY the .md file content starting with ---.", prompt)

	msgs := []provider.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}

	content, err := provider.ChatCompletion(ctx, s.currentProvider(), msgs)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	content = stripCodeFences(content)

	if _, err := mdfmt.ParseBytes([]byte(content)); err != nil {
		return "", fmt.Errorf("generated content is not a valid skill definition: %w", err)
	}

	return content, nil
}

// ListGraphs returns all loaded graph definitions, ordered by id.
func (s *LocalService) ListGraphs(_ context.Context) ([]GraphDefinition, error) {
	if s.cfg.GraphCatalog == nil {
		return nil, nil
	}
	defs := s.cfg.GraphCatalog.Graphs()
	out := make([]GraphDefinition, 0, len(defs))
	for _, d := range defs {
		out = append(out, graphexecDefinitionToService(d))
	}
	return out, nil
}

// GetGraph returns a single graph definition by id.
func (s *LocalService) GetGraph(_ context.Context, id string) (GraphDefinition, error) {
	if s.cfg.GraphCatalog == nil {
		return GraphDefinition{}, fmt.Errorf("getting graph %s: %w", id, ErrNotFound)
	}
	for _, d := range s.cfg.GraphCatalog.Graphs() {
		if d.ID == id {
			return graphexecDefinitionToService(d), nil
		}
	}
	return GraphDefinition{}, fmt.Errorf("getting graph %s: %w", id, ErrNotFound)
}

// graphexecDefinitionToService converts a graphexec.Definition (the YAML-loaded
// internal shape) to a service.GraphDefinition (the TUI-facing DTO). The edge
// conversion expands routers into one conditional edge per branch so renderers
// can draw each branch distinctly.
func graphexecDefinitionToService(d *graphexec.Definition) GraphDefinition {
	out := GraphDefinition{
		ID:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Tags:        append([]string(nil), d.Tags...),
		Entry:       d.Entry,
		Exit:        d.Exit,
	}
	for _, n := range d.Nodes {
		out.Nodes = append(out.Nodes, n.ID)
	}
	for _, e := range d.Edges {
		if e.Router == nil {
			out.Edges = append(out.Edges, GraphEdge{
				From: e.From,
				To:   mapEndSentinel(e.To),
				Kind: GraphEdgeStatic,
			})
			continue
		}
		for _, b := range e.Router.Branches {
			out.Edges = append(out.Edges, GraphEdge{
				From:  e.From,
				To:    mapEndSentinel(b.To),
				Kind:  GraphEdgeConditional,
				Label: fmt.Sprintf("%v", b.When),
			})
		}
		if e.Router.Default != "" {
			out.Edges = append(out.Edges, GraphEdge{
				From:  e.From,
				To:    mapEndSentinel(e.Router.Default),
				Kind:  GraphEdgeConditional,
				Label: "default",
			})
		}
	}
	return out
}

// mapEndSentinel maps the YAML "end" sentinel to the empty string so renderers
// treat it as a terminal edge without depending on graphexec's constant.
func mapEndSentinel(to string) string {
	if to == graphexec.EndNode {
		return ""
	}
	return to
}

// writeGeneratedSkillFile writes LLM-generated skill content to the user skills directory.
func (s *LocalService) writeGeneratedSkillFile(content string) (string, error) {
	skillsDir := filepath.Join(s.cfg.ConfigDir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", fmt.Errorf("creating skills dir: %w", err)
	}

	slug := "generated-skill"
	if skillDef, err := mdfmt.ParseBytes([]byte(content)); err == nil && skillDef.Name != "" {
		nameSlug := loader.Slugify(skillDef.Name)
		if nameSlug != "" {
			slug = nameSlug
		}
	}

	path := filepath.Join(skillsDir, slug+".md")
	if _, err := os.Stat(path); err == nil {
		found := false
		for i := 2; i < 1000; i++ {
			candidate := filepath.Join(skillsDir, fmt.Sprintf("%s-%d.md", slug, i))
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				path = candidate
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("too many skill files with slug %q", slug)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing skill file: %w", err)
	}
	return path, nil
}

// stripCodeFences removes markdown code fences from LLM output.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		idx := strings.Index(s, "\n")
		if idx != -1 {
			s = s[idx+1:]
		}
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
