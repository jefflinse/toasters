package service

import (
	"context"
	"encoding/json"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

func dbJobToService(j *db.Job) Job {
	return Job{
		ID:           j.ID,
		Title:        j.Title,
		Description:  j.Description,
		Type:         j.Type,
		Status:       JobStatus(j.Status),
		WorkspaceDir: j.WorkspaceDir,
		CreatedAt:    j.CreatedAt,
		UpdatedAt:    j.UpdatedAt,
		Metadata:     j.Metadata,
	}
}

func dbTaskToService(t *db.Task) Task {
	return Task{
		ID:              t.ID,
		JobID:           t.JobID,
		Title:           t.Title,
		Status:          TaskStatus(t.Status),
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

func dbProgressToService(p *db.ProgressReport) ProgressReport {
	return ProgressReport{
		ID:        p.ID,
		JobID:     p.JobID,
		TaskID:    p.TaskID,
		WorkerID:  p.WorkerID,
		Status:    p.Status,
		Message:   p.Message,
		CreatedAt: p.CreatedAt,
	}
}

func dbSkillToService(sk *db.Skill) Skill {
	var tools []string
	if len(sk.Tools) > 0 {
		_ = json.Unmarshal(sk.Tools, &tools)
	}
	return Skill{
		ID:          sk.ID,
		Name:        sk.Name,
		Description: sk.Description,
		Tools:       tools,
		Prompt:      sk.Prompt,
		Source:      sk.Source,
		SourcePath:  sk.SourcePath,
		CreatedAt:   sk.CreatedAt,
		UpdatedAt:   sk.UpdatedAt,
	}
}

func dbWorkerSessionToService(s *db.WorkerSession) WorkerSession {
	return WorkerSession{
		ID:        s.ID,
		WorkerID:  s.WorkerID,
		JobID:     s.JobID,
		TaskID:    s.TaskID,
		Status:    SessionStatus(s.Status),
		Model:     s.Model,
		Provider:  s.Provider,
		TokensIn:  s.TokensIn,
		TokensOut: s.TokensOut,
		StartedAt: s.StartedAt,
		EndedAt:   s.EndedAt,
		CostUSD:   s.CostUSD,
	}
}

func runtimeSnapshotToService(snap runtime.SessionSnapshot) SessionSnapshot {
	return SessionSnapshot{
		ID:                   snap.ID,
		WorkerID:             snap.WorkerID,
		JobID:                snap.JobID,
		TaskID:               snap.TaskID,
		Status:               snap.Status,
		Model:                snap.Model,
		Provider:             snap.Provider,
		StartTime:            snap.StartTime,
		TokensIn:             snap.TokensIn,
		TokensOut:            snap.TokensOut,
		CurrentContextTokens: snap.CurrentContextTokens,
	}
}

// sessionSnapshotsToService maps runtime snapshots to service DTOs and fills
// in each one's resolved context window. The window is resolved once per
// provider/model pair, not per session — snapshot builds run on a 500ms
// broadcast cadence and a fleet typically shares one model.
func (s *LocalService) sessionSnapshotsToService(ctx context.Context, snaps []runtime.SessionSnapshot) []SessionSnapshot {
	type provModel struct{ provider, model string }
	memo := make(map[provModel]int)
	out := make([]SessionSnapshot, 0, len(snaps))
	for _, snap := range snaps {
		dto := runtimeSnapshotToService(snap)
		if s.cfg.ContextWindows != nil {
			key := provModel{snap.Provider, snap.Model}
			w, ok := memo[key]
			if !ok {
				w = s.cfg.ContextWindows.Window(ctx, snap.Provider, snap.Model)
				memo[key] = w
			}
			dto.ContextWindow = w
		}
		out = append(out, dto)
	}
	return out
}

func dbFeedEntryToService(fe *db.FeedEntry) FeedEntry {
	return FeedEntry{
		ID:        fe.ID,
		JobID:     fe.JobID,
		EntryType: FeedEntryType(fe.EntryType),
		Content:   fe.Content,
		Metadata:  fe.Metadata,
		CreatedAt: fe.CreatedAt,
	}
}

func mcpServerStatusToService(ss mcp.ServerStatus) MCPServerStatus {
	var state MCPServerState
	switch ss.State {
	case mcp.ServerConnected:
		state = MCPServerStateConnected
	case mcp.ServerFailed:
		state = MCPServerStateFailed
	default:
		state = MCPServerStateFailed
	}

	tools := make([]MCPToolInfo, 0, len(ss.Tools))
	for _, t := range ss.Tools {
		tools = append(tools, MCPToolInfo{
			NamespacedName: t.NamespacedName,
			OriginalName:   t.OriginalName,
			ServerName:     t.ServerName,
			Description:    t.Description,
			InputSchema:    t.InputSchema,
		})
	}

	return MCPServerStatus{
		Name:      ss.Name,
		Transport: ss.Transport,
		State:     state,
		Error:     ss.Error,
		ToolCount: ss.ToolCount,
		Tools:     tools,
	}
}

func providerModelInfoToService(m provider.ModelInfo) ModelInfo {
	// State is a TUI affordance indicating the loaded/preferred model. The
	// upstream mycelium ModelInfo no longer carries it; until a replacement
	// signal is wired through, this field is left empty. The TUI degrades
	// to showing the first model rather than the loaded one.
	return ModelInfo{
		ID:                  m.ID,
		Name:                m.Name,
		Provider:            m.Provider,
		MaxContextLength:    m.MaxContextLength,
		LoadedContextLength: m.LoadedContextLength,
	}
}
