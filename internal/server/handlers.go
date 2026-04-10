package server

import (
	"fmt"
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

// maxResponseBytes is the maximum allowed prompt/blocker response length in bytes.
// Strictly below the service layer's 51200 byte limit.
const maxResponseBytes = 50_000

// maxBlockerAnswers is the maximum number of blocker answers allowed.
const maxBlockerAnswers = 50

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

// respondToBlocker handles POST /api/v1/operator/blockers/{jobId}/{taskId}/respond.
func (s *Server) respondToBlocker(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobId")
	taskID := r.PathValue("taskId")
	if jobID == "" || taskID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "jobId and taskId are required")
		return
	}

	var req RespondToBlockerRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Answers) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "answers array is required and must not be empty")
		return
	}
	if len(req.Answers) > maxBlockerAnswers {
		writeError(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("too many answers: %d exceeds maximum %d", len(req.Answers), maxBlockerAnswers))
		return
	}
	for i, a := range req.Answers {
		if strings.TrimSpace(a) == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("answer at index %d must not be empty", i))
			return
		}
		if len(a) > maxResponseBytes {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("answer at index %d too long: %d bytes exceeds maximum %d", i, len(a), maxResponseBytes))
			return
		}
	}

	if err := s.svc.Operator().RespondToBlocker(r.Context(), jobID, taskID, req.Answers); err != nil {
		handleServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
// Agents handlers
// ---------------------------------------------------------------------------

// listAgents handles GET /api/v1/agents.
func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	pg, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	agents, err := s.svc.Definitions().ListAgents(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireAgents := make([]wireAgent, 0, len(agents))
	for _, a := range agents {
		wireAgents = append(wireAgents, agentToWire(a))
	}

	page, total := paginate(wireAgents, pg)
	writeJSON(w, http.StatusOK, PaginatedResponse[wireAgent]{Items: page, Total: total})
}

// getAgent handles GET /api/v1/agents/{id}.
func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := s.svc.Definitions().GetAgent(r.Context(), id)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, agentToWire(a))
}

// createAgent handles POST /api/v1/agents.
func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var req CreateAgentRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}

	a, err := s.svc.Definitions().CreateAgent(r.Context(), req.Name)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/api/v1/agents/%s", a.ID))
	writeJSON(w, http.StatusCreated, agentToWire(a))
}

// deleteAgent handles DELETE /api/v1/agents/{id}.
func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.Definitions().DeleteAgent(r.Context(), id); err != nil {
		handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addSkillToAgent handles POST /api/v1/agents/{id}/skills.
func (s *Server) addSkillToAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req AddSkillToAgentRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.SkillName) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "skill_name is required")
		return
	}

	if err := s.svc.Definitions().AddSkillToAgent(r.Context(), id, req.SkillName); err != nil {
		handleServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// generateAgent handles POST /api/v1/agents/generate.
func (s *Server) generateAgent(w http.ResponseWriter, r *http.Request) {
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

	opID, err := s.svc.Definitions().GenerateAgent(r.Context(), req.Prompt)
	if err != nil {
		setRetryAfterIfRateLimited(w, err)
		handleServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, AsyncResponse{OperationID: opID})
}

// ---------------------------------------------------------------------------
// Teams handlers
// ---------------------------------------------------------------------------

// listTeams handles GET /api/v1/teams.
func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	pg, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	teams, err := s.svc.Definitions().ListTeams(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	wireTeams := make([]wireTeamView, 0, len(teams))
	for _, t := range teams {
		wireTeams = append(wireTeams, teamViewToWire(t))
	}

	page, total := paginate(wireTeams, pg)
	writeJSON(w, http.StatusOK, PaginatedResponse[wireTeamView]{Items: page, Total: total})
}

// getTeam handles GET /api/v1/teams/{id}.
func (s *Server) getTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tv, err := s.svc.Definitions().GetTeam(r.Context(), id)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, teamViewToWire(tv))
}

// createTeam handles POST /api/v1/teams.
func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	var req CreateTeamRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}

	tv, err := s.svc.Definitions().CreateTeam(r.Context(), req.Name)
	if err != nil {
		handleServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/api/v1/teams/%s", tv.Team.ID))
	writeJSON(w, http.StatusCreated, teamViewToWire(tv))
}

// deleteTeam handles DELETE /api/v1/teams/{id}.
func (s *Server) deleteTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.Definitions().DeleteTeam(r.Context(), id); err != nil {
		handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addAgentToTeam handles POST /api/v1/teams/{id}/agents.
func (s *Server) addAgentToTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req AddAgentToTeamRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.AgentID) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "agent_id is required")
		return
	}

	if err := s.svc.Definitions().AddAgentToTeam(r.Context(), id, req.AgentID); err != nil {
		handleServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// setCoordinator handles PUT /api/v1/teams/{id}/coordinator.
func (s *Server) setCoordinator(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req SetCoordinatorRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.AgentName) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "agent_name is required")
		return
	}

	if err := s.svc.Definitions().SetCoordinator(r.Context(), id, req.AgentName); err != nil {
		handleServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// promoteTeam handles POST /api/v1/teams/{id}/promote.
func (s *Server) promoteTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	opID, err := s.svc.Definitions().PromoteTeam(r.Context(), id)
	if err != nil {
		setRetryAfterIfRateLimited(w, err)
		handleServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, AsyncResponse{OperationID: opID})
}

// generateTeam handles POST /api/v1/teams/generate.
func (s *Server) generateTeam(w http.ResponseWriter, r *http.Request) {
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

	opID, err := s.svc.Definitions().GenerateTeam(r.Context(), req.Prompt)
	if err != nil {
		setRetryAfterIfRateLimited(w, err)
		handleServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, AsyncResponse{OperationID: opID})
}

// detectCoordinator handles POST /api/v1/teams/{id}/detect-coordinator.
func (s *Server) detectCoordinator(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	opID, err := s.svc.Definitions().DetectCoordinator(r.Context(), id)
	if err != nil {
		setRetryAfterIfRateLimited(w, err)
		handleServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, AsyncResponse{OperationID: opID})
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

// getLogs handles GET /api/v1/logs.
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	content, err := s.svc.System().GetLogs(r.Context())
	if err != nil {
		handleServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, logsResponse{Content: content})
}
