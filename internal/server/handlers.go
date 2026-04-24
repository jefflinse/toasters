package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jefflinse/toasters/internal/service"
)

// maxMessageBytes is the maximum allowed message length in bytes.
// Strictly below the service layer's 102400 byte limit to ensure
// the server always rejects before the service does.
const maxMessageBytes = 100_000

// maxPromptBytes is the maximum allowed generation prompt length in bytes.
// Strictly below the service layer's 51200 byte limit.
const maxPromptBytes = 10_000

// maxResponseBytes is the maximum allowed prompt response length in bytes.
// Strictly below the service layer's 51200 byte limit.
const maxResponseBytes = 50_000

// validJobStatuses is the set of valid job status filter values.
var validJobStatuses = map[string]bool{
	"pending": true, "setting_up": true, "decomposing": true,
	"active": true, "paused": true, "completed": true,
	"failed": true, "cancelled": true,
}

// ---------------------------------------------------------------------------
// Operator handlers
// ---------------------------------------------------------------------------

// sendMessage handles POST /api/v1/operator/messages.
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req SendMessageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "message is required")
		return
	}
	if len(req.Message) > maxMessageBytes {
		writeError(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("message too long: %d bytes exceeds maximum %d", len(req.Message), maxMessageBytes))
		return
	}

	turnID, err := s.svc.Operator().SendMessage(r.Context(), req.Message)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, TurnResponse{TurnID: turnID})
}

// respondToPrompt handles POST /api/v1/operator/prompts/{requestId}/respond.
func (s *Server) respondToPrompt(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("requestId")
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "requestId is required")
		return
	}

	var req RespondToPromptRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Response) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "response is required")
		return
	}
	if len(req.Response) > maxResponseBytes {
		writeError(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("response too long: %d bytes exceeds maximum %d", len(req.Response), maxResponseBytes))
		return
	}

	if err := s.svc.Operator().RespondToPrompt(r.Context(), requestID, req.Response); err != nil {
		handleServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// operatorStatus handles GET /api/v1/operator/status.
func (s *Server) operatorStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.Operator().Status(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, OperatorStatusResponse{
		State:         string(st.State),
		CurrentTurnID: st.CurrentTurnID,
		ModelName:     st.ModelName,
		Endpoint:      st.Endpoint,
	})
}

// operatorHistory handles GET /api/v1/operator/history.
func (s *Server) operatorHistory(w http.ResponseWriter, r *http.Request) {
	entries, err := s.svc.Operator().History(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireEntries := make([]wireChatEntry, 0, len(entries))
	for _, e := range entries {
		wireEntries = append(wireEntries, chatEntryToWire(e))
	}

	writeJSON(w, http.StatusOK, PaginatedResponse[wireChatEntry]{
		Items: wireEntries,
		Total: len(wireEntries),
	})
}

// ---------------------------------------------------------------------------
// Skills handlers
// ---------------------------------------------------------------------------

// listSkills handles GET /api/v1/skills.
func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	pg, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	skills, err := s.svc.Definitions().ListSkills(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireSkills := make([]wireSkill, 0, len(skills))
	for _, sk := range skills {
		wireSkills = append(wireSkills, skillToWire(sk))
	}

	page, total := paginate(wireSkills, pg)
	writeJSON(w, http.StatusOK, PaginatedResponse[wireSkill]{Items: page, Total: total})
}

// getSkill handles GET /api/v1/skills/{id}.
func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sk, err := s.svc.Definitions().GetSkill(r.Context(), id)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, skillToWire(sk))
}

// createSkill handles POST /api/v1/skills.
func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	var req CreateSkillRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}

	sk, err := s.svc.Definitions().CreateSkill(r.Context(), req.Name)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/api/v1/skills/%s", sk.ID))
	writeJSON(w, http.StatusCreated, skillToWire(sk))
}

// deleteSkill handles DELETE /api/v1/skills/{id}.
func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.Definitions().DeleteSkill(r.Context(), id); err != nil {
		handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// generateSkill handles POST /api/v1/skills/generate.
func (s *Server) generateSkill(w http.ResponseWriter, r *http.Request) {
	var req GenerateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt is required")
		return
	}
	if len(req.Prompt) > maxPromptBytes {
		writeError(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("prompt too long: %d bytes exceeds maximum %d", len(req.Prompt), maxPromptBytes))
		return
	}

	opID, err := s.svc.Definitions().GenerateSkill(r.Context(), req.Prompt)
	if err != nil {
		setRetryAfterIfRateLimited(w, err)
		handleServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, AsyncResponse{OperationID: opID})
}

// ---------------------------------------------------------------------------
// Workers handlers
// ---------------------------------------------------------------------------

// listWorkers handles GET /api/v1/workers.
func (s *Server) listWorkers(w http.ResponseWriter, r *http.Request) {
	pg, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	workers, err := s.svc.Definitions().ListWorkers(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireWorkers := make([]wireWorker, 0, len(workers))
	for _, a := range workers {
		wireWorkers = append(wireWorkers, workerToWire(a))
	}

	page, total := paginate(wireWorkers, pg)
	writeJSON(w, http.StatusOK, PaginatedResponse[wireWorker]{Items: page, Total: total})
}

// getWorker handles GET /api/v1/workers/{id}.
func (s *Server) getWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := s.svc.Definitions().GetWorker(r.Context(), id)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, workerToWire(a))
}

// ---------------------------------------------------------------------------
// Graphs handlers
// ---------------------------------------------------------------------------

// listGraphs handles GET /api/v1/graphs.
func (s *Server) listGraphs(w http.ResponseWriter, r *http.Request) {
	pg, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	graphs, err := s.svc.Definitions().ListGraphs(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	items := make([]wireGraphDefinition, 0, len(graphs))
	for _, g := range graphs {
		items = append(items, graphDefinitionToWire(g))
	}

	page, total := paginate(items, pg)
	writeJSON(w, http.StatusOK, PaginatedResponse[wireGraphDefinition]{Items: page, Total: total})
}

// getGraph handles GET /api/v1/graphs/{id}.
func (s *Server) getGraph(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := s.svc.Definitions().GetGraph(r.Context(), id)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, graphDefinitionToWire(g))
}

// ---------------------------------------------------------------------------
// Jobs handlers
// ---------------------------------------------------------------------------

// listJobs handles GET /api/v1/jobs.
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Check for ?all=true — maps to ListAll().
	if q.Get("all") == "true" {
		jobs, err := s.svc.Jobs().ListAll(r.Context())
		if err != nil {
			handleServiceError(w, r, err)
			return
		}
		wireJobs := make([]wireJob, 0, len(jobs))
		for _, j := range jobs {
			wireJobs = append(wireJobs, jobToWire(j))
		}
		writeJSON(w, http.StatusOK, PaginatedResponse[wireJob]{Items: wireJobs, Total: len(wireJobs)})
		return
	}

	pg, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	filter := &service.JobListFilter{
		Limit:  pg.Limit,
		Offset: pg.Offset,
	}

	if statusStr := q.Get("status"); statusStr != "" {
		if !validJobStatuses[statusStr] {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("invalid status filter: %q", statusStr))
			return
		}
		st := service.JobStatus(statusStr)
		filter.Status = &st
	}

	if typeStr := q.Get("type"); typeStr != "" {
		filter.Type = &typeStr
	}

	jobs, err := s.svc.Jobs().List(r.Context(), filter)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireJobs := make([]wireJob, 0, len(jobs))
	for _, j := range jobs {
		wireJobs = append(wireJobs, jobToWire(j))
	}

	// Note: The service layer handles pagination via filter.Limit/Offset,
	// so we return the results directly. Total is the count of returned items
	// since the service doesn't return a separate total count.
	writeJSON(w, http.StatusOK, PaginatedResponse[wireJob]{Items: wireJobs, Total: len(wireJobs)})
}

// getJob handles GET /api/v1/jobs/{id}.
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	jd, err := s.svc.Jobs().Get(r.Context(), id)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobDetailToWire(jd))
}

// cancelJob handles POST /api/v1/jobs/{id}/cancel.
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.Jobs().Cancel(r.Context(), id); err != nil {
		handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Sessions handlers
// ---------------------------------------------------------------------------

// listSessions handles GET /api/v1/sessions.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	pg, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	snaps, err := s.svc.Sessions().List(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireSnaps := make([]wireSessionSnapshot, 0, len(snaps))
	for _, snap := range snaps {
		wireSnaps = append(wireSnaps, sessionSnapshotToWire(snap))
	}

	page, total := paginate(wireSnaps, pg)
	writeJSON(w, http.StatusOK, PaginatedResponse[wireSessionSnapshot]{Items: page, Total: total})
}

// getSession handles GET /api/v1/sessions/{id}.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sd, err := s.svc.Sessions().Get(r.Context(), id)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionDetailToWire(sd))
}

// cancelSession handles POST /api/v1/sessions/{id}/cancel.
func (s *Server) cancelSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.Sessions().Cancel(r.Context(), id); err != nil {
		handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// System handlers
// ---------------------------------------------------------------------------

// health handles GET /api/v1/health.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	h, err := s.svc.System().Health(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:        h.Status,
		Version:       h.Version,
		UptimeSeconds: h.Uptime.Seconds(),
	})
}

// listModels handles GET /api/v1/models.
func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.svc.System().ListModels(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireModels := make([]wireModelInfo, 0, len(models))
	for _, m := range models {
		wireModels = append(wireModels, modelInfoToWire(m))
	}

	writeJSON(w, http.StatusOK, PaginatedResponse[wireModelInfo]{Items: wireModels, Total: len(wireModels)})
}

// addProvider handles POST /api/v1/providers.
func (s *Server) addProvider(w http.ResponseWriter, r *http.Request) {
	var req wireAddProviderRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.svc.System().AddProvider(r.Context(), service.AddProviderRequest{
		ID:       req.ID,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		APIKey:   req.APIKey,
	}); err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

// updateProvider handles PUT /api/v1/providers.
func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request) {
	var req wireAddProviderRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.svc.System().UpdateProvider(r.Context(), service.AddProviderRequest{
		ID:       req.ID,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		APIKey:   req.APIKey,
	}); err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// listConfiguredProviderIDs handles GET /api/v1/providers/configured.
func (s *Server) listConfiguredProviderIDs(w http.ResponseWriter, r *http.Request) {
	ids, err := s.svc.System().ListConfiguredProviderIDs(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ids)
}

// setOperatorProvider handles PUT /api/v1/operator/provider.
func (s *Server) setOperatorProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderID string `json:"provider_id"`
		Model      string `json:"model"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.svc.System().SetOperatorProvider(r.Context(), req.ProviderID, req.Model); err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// listProviderModels handles GET /api/v1/providers/{id}/models.
func (s *Server) listProviderModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	models, err := s.svc.System().ListProviderModels(r.Context(), id)
	if err != nil {
		// Provider model listing errors are user-actionable (provider unreachable,
		// misconfigured, etc.), not internal bugs. Surface the message as a 502
		// so the TUI can display it instead of a generic "internal server error".
		slog.Warn("failed to list provider models", "provider", id, "error", err)
		writeError(w, http.StatusBadGateway, "provider_error", service.SanitizeErrorMessage(err.Error()))
		return
	}
	wireModels := make([]wireModelInfo, 0, len(models))
	for _, m := range models {
		wireModels = append(wireModels, modelInfoToWire(m))
	}
	writeJSON(w, http.StatusOK, PaginatedResponse[wireModelInfo]{Items: wireModels, Total: len(wireModels)})
}

// listCatalog handles GET /api/v1/catalog.
func (s *Server) listCatalog(w http.ResponseWriter, r *http.Request) {
	providers, err := s.svc.System().ListCatalogProviders(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireProviders := make([]wireCatalogProvider, 0, len(providers))
	for _, p := range providers {
		wireProviders = append(wireProviders, catalogProviderToWire(p))
	}

	writeJSON(w, http.StatusOK, PaginatedResponse[wireCatalogProvider]{Items: wireProviders, Total: len(wireProviders)})
}

// listMCPServers handles GET /api/v1/mcp/servers.
func (s *Server) listMCPServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.svc.System().ListMCPServers(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireServers := make([]wireMCPServerStatus, 0, len(servers))
	for _, srv := range servers {
		wireServers = append(wireServers, mcpServerStatusToWire(srv))
	}

	writeJSON(w, http.StatusOK, PaginatedResponse[wireMCPServerStatus]{Items: wireServers, Total: len(wireServers)})
}

// getProgress handles GET /api/v1/progress.
func (s *Server) getProgress(w http.ResponseWriter, r *http.Request) {
	ps, err := s.svc.System().GetProgressState(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, progressStateToWire(ps))
}

// getSettings handles GET /api/v1/settings.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.svc.System().GetSettings(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// updateSettings handles PUT /api/v1/settings.
func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var req service.Settings
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.svc.System().UpdateSettings(r.Context(), req); err != nil {
		// Validation errors are user-actionable — 400 rather than the generic 500.
		writeError(w, http.StatusBadRequest, "bad_request", service.SanitizeErrorMessage(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// getLogs handles GET /api/v1/logs.
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	content, err := s.svc.System().GetLogs(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, logsResponse{Content: content})
}
