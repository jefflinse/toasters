package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jefflinse/toasters/internal/service"
)

func (m *Model) handleModels(msg ModelsMsg) (tea.Model, tea.Cmd) {
	// ListModels is a non-essential capability check. The model name
	// and endpoint already come from Operator().Status() during
	// AppReadyMsg, and the chat works whether or not the provider
	// supports a model listing endpoint. So when the call fails:
	//   - log a warning so a real failure is debuggable
	//   - flip the sidebar Connected indicator (it's the only signal
	//     of "the operator's provider is reachable")
	//   - DO NOT set m.err — surfacing a non-fatal capability check
	//     as a chat error makes the whole TUI look broken when in
	//     fact the operator is fully functional.
	if msg.Err != nil {
		m.stats.Connected = false
		slog.Warn("ListModels failed; sidebar context length unavailable", "error", msg.Err)
	} else {
		m.stats.Connected = true
		// Build a model-ID → context-length map so the fleet pane can size a
		// context bar per worker (worker sessions carry only the model name).
		if m.modelContext == nil {
			m.modelContext = make(map[string]int, len(msg.Models))
		}
		for _, mi := range msg.Models {
			if cl := mi.ContextLength(); cl > 0 {
				m.modelContext[mi.ID] = cl
			}
		}
		if len(msg.Models) > 0 {
			if m.stats.ModelName != "" {
				// We already have a configured model name from
				// AppReadyMsg. If the server didn't resolve a context
				// window for it, try to find one from the list — but never
				// overwrite the name itself (provider IDs, e.g. LM Studio
				// filenames, often don't match the canonical config value)
				// or a server-resolved window (it factors in overrides and
				// the catalog, which this lookup doesn't).
				if m.stats.ContextLength == 0 {
					for _, mi := range msg.Models {
						if mi.ID == m.stats.ModelName {
							m.stats.ContextLength = mi.ContextLength()
							break
						}
					}
				}
			} else {
				// No configured name yet — fall back to a "loaded" model,
				// or the first one in the list.
				picked := msg.Models[0]
				for _, mi := range msg.Models {
					if mi.State == "loaded" {
						picked = mi
						break
					}
				}
				m.stats.ModelName = picked.ID
				m.stats.ContextLength = picked.ContextLength()
			}
		}
	}
	m.updateViewportContent()
	return m, nil
}

func (m *Model) handleAppReady(msg AppReadyMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	m.initMessages()
	m.loading = false
	m.operatorDisabled = msg.OperatorDisabled

	if msg.OperatorDisabled {
		// Operator is not configured — show setup message.
		m.appendEntry(service.ChatEntry{
			Message: service.ChatMessage{
				Role:    service.MessageRoleAssistant,
				Content: "No operator provider is configured. Use `/providers` to add a provider, then `/operator` to select it.",
			},
			Timestamp: time.Now(),
		})
		m.updateViewportContent()
		return m, tea.Batch(cmds...)
	}

	// Hydrate sidebar fields from the server-provided operator status so
	// they reflect the canonical configured values (rather than e.g. an
	// LM Studio filename that ListModels would return). Set these BEFORE
	// the ListModels response arrives so the model picker doesn't clobber.
	if msg.ModelName != "" {
		m.stats.ModelName = msg.ModelName
	}
	if msg.Endpoint != "" {
		m.stats.Endpoint = msg.Endpoint
	}
	if msg.ContextWindow > 0 {
		m.stats.ContextLength = msg.ContextWindow
	}
	// Hydrate persisted chat history from the server. This survives
	// server restarts so the user picks up where they left off.
	for _, entry := range msg.History {
		m.appendEntry(entry)
	}
	// Hydrate pending blockers so the Blockers panel reflects work that's
	// still waiting on the user across a reconnect.
	m.blockers = msg.Blockers
	if m.blockersSel >= len(m.blockers) {
		m.blockersSel = 0
	}
	// Inject the pre-fetched greeting directly — no stream, no flash.
	// Only fire a greeting when no history exists; otherwise it would
	// look stale on top of a real conversation.
	if msg.Greeting != "" && len(msg.History) == 0 {
		m.appendEntry(service.ChatEntry{
			Message: service.ChatMessage{
				Role:    service.MessageRoleAssistant,
				Content: msg.Greeting,
			},
			Timestamp: time.Now(),
		})
	}
	m.updateViewportContent()
	return m, tea.Batch(cmds...)
}

func (m *Model) handleSessionDone(msg SessionDoneMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	slot, ok := m.runtimeSessions[msg.SessionID]
	if !ok {
		return m, nil
	}
	slot.status = msg.Status
	slot.endTime = time.Now()
	m.markWorkerStreamDone(msg.SessionID)
	m.updateViewportContent()
	// Toast reflects the real terminal status rather than always "done":
	// failures and cancellations read very differently to an operator
	// watching a long autonomous run.
	switch msg.Status {
	case "failed":
		cmds = append(cmds, m.addToast("✗ "+msg.WorkerName+" failed", toastError))
	case "cancelled":
		cmds = append(cmds, m.addToast("— "+msg.WorkerName+" cancelled", toastInfo))
	default:
		cmds = append(cmds, m.addToast("🍞 "+msg.WorkerName+" finished", toastSuccess))
	}
	// Note: worker completion is no longer reported back to the operator from
	// the TUI. The server is responsible for routing task completion into the
	// operator's event channel. The TUI is a viewer, not a router.
	return m, tea.Batch(cmds...)
}

func (m *Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	// Click-to-focus: route clicks to the appropriate panel.
	// Don't steal clicks when any overlay is active.
	if !m.skillsModal.show &&
		!m.mcpModal.show && !m.catalogModal.show && !m.operatorModal.show &&
		!m.nodes.show && !m.loading {
		if m.shouldShowSidebar() && m.pointInSidebar(msg.X) {
			// Clicked the sidebar — determine which of the three panes was
			// clicked. Pane order (top to bottom): Jobs, Fleet, Blockers.
			// Boundaries come from the same height model the renderer uses.
			jobsH, fleetH, _ := m.sidebarPaneHeights(m.sidebarWidth, m.height)
			paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
			jobsBottom := jobsH + paneFrameV
			fleetBottom := jobsBottom + fleetH + paneFrameV
			switch {
			case msg.Y < jobsBottom:
				if m.focused != focusJobs {
					cmds = append(cmds, m.setFocus(focusJobs))
					m.input.Blur()
				}
			case msg.Y < fleetBottom:
				if m.focused != focusFleet {
					cmds = append(cmds, m.setFocus(focusFleet))
					m.input.Blur()
				}
			default:
				if m.focused != focusBlockers {
					cmds = append(cmds, m.setFocus(focusBlockers))
					m.input.Blur()
				}
			}
		} else {
			// Clicked chat area — focus chat.
			if m.focused != focusChat {
				cmds = append(cmds, m.setFocus(focusChat))
				cmds = append(cmds, m.input.Focus())
			}
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	// Jobs modal: route wheel events to the panel under the cursor so
	// the task list and graph list stay usable with the mouse.
	if m.jobsModal.show {
		m.scrollJobsModal(msg)
		return m, nil
	}
	// Forward mouse wheel events to viewport for scroll support.
	var cmd tea.Cmd
	m.chatViewport, cmd = m.chatViewport.Update(msg)
	cmds = append(cmds, cmd)
	cmds = append(cmds, m.showScrollbar())
	// Track whether user has scrolled away from the bottom.
	if m.chatViewport.AtBottom() {
		m.scroll.userScrolled = false
		m.scroll.hasNewMessages = false
	} else {
		m.scroll.userScrolled = true
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleSpinnerTick(msg spinnerTickMsg) (tea.Model, tea.Cmd) {
	m.spinnerFrame++
	// Re-arm as long as something is animating: operator streaming, any
	// worker running, any displayed job still active/pending, or a
	// sidebar panel whose title rainbow-cycles while focused. Animating
	// indicators should keep moving even when the pane isn't focused.
	needTick := m.stream.streaming
	if !needTick {
		for _, rs := range m.runtimeSessions {
			if rs.status == "active" {
				needTick = true
				break
			}
		}
	}
	if !needTick {
		for _, j := range m.displayJobs() {
			if j.Status == service.JobStatusActive || j.Status == service.JobStatusPending {
				needTick = true
				break
			}
		}
	}
	if !needTick && (m.focused == focusJobs || m.focused == focusFleet) {
		needTick = true
	}
	// Jobs modal's focused panel also rainbow-cycles its title.
	if !needTick && m.jobsModal.show {
		needTick = true
	}
	if needTick {
		m.spinnerRunning = true
		return m, spinnerTick()
	}
	m.spinnerRunning = false
	return m, nil
}

func (m *Model) handleMCPStatus(msg MCPStatusMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var toastCmds []tea.Cmd
	var connectedCount, totalTools int
	for _, s := range msg.Servers {
		switch s.State {
		case service.MCPServerStateConnected:
			connectedCount++
			totalTools += s.ToolCount
		case service.MCPServerStateFailed:
			toastCmds = append(toastCmds, m.addToast(
				fmt.Sprintf("⚠ MCP: %s failed", s.Name),
				toastWarning,
			))
		}
	}
	if connectedCount > 0 {
		serverWord := "servers"
		if connectedCount == 1 {
			serverWord = "server"
		}
		toolWord := "tools"
		if totalTools == 1 {
			toolWord = "tool"
		}
		toastCmds = append(toastCmds, m.addToast(
			fmt.Sprintf("🔌 MCP: %d %s, %d %s", connectedCount, serverWord, totalTools, toolWord),
			toastInfo,
		))
	}
	cmds = append(cmds, toastCmds...)
	return m, tea.Batch(cmds...)
}

func (m *Model) handleConnectionRestored(msg ConnectionRestoredMsg) (tea.Model, tea.Cmd) {
	m.stats.Connected = true
	// Refetch the authoritative blocker set: blocker events that fired
	// during the outage were never delivered, so a HITL prompt raised
	// while disconnected would otherwise stay invisible (and resolved
	// ones would linger). Progress state is already resynced by the
	// client's synthetic progress.update.
	svc := m.svc
	resyncBlockers := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		blockers, err := svc.Operator().Blockers(ctx)
		if err != nil {
			slog.Warn("failed to refetch blockers after reconnect", "error", err)
			return nil
		}
		return BlockersResyncMsg{Blockers: blockers}
	}
	// Refetch chat history too: operator text streamed during the outage
	// was never delivered, so the conversation has a hole the SSE replay
	// ring may not cover (long outages).
	resyncChat := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		history, err := svc.Operator().History(ctx)
		if err != nil {
			slog.Warn("failed to refetch chat history after reconnect", "error", err)
			return nil
		}
		return ChatResyncMsg{History: history}
	}
	return m, tea.Batch(m.addToast("Server connection restored", toastSuccess), resyncBlockers, resyncChat)
}

func (m *Model) handleOperatorToolCall(msg OperatorToolCallMsg) (tea.Model, tea.Cmd) {
	slog.Debug("operator tool call", "tool", msg.Name, "error", msg.IsError)
	// Commit any text streamed before this tool call so the chat stays
	// chronological: text, then the tool indicator, then any following text.
	m.flushOperatorStream()
	content := "`" + msg.Name + "`"
	if r := strings.TrimSpace(msg.Result); r != "" {
		marker := "→ "
		if msg.IsError {
			marker = "✗ "
		}
		content += "\n" + marker + r
	}
	m.appendEntry(service.ChatEntry{
		Message: service.ChatMessage{
			Role:    service.MessageRoleAssistant,
			Content: content,
		},
		Timestamp:  time.Now(),
		ClaudeMeta: "tool-call-indicator",
	})
	m.updateViewportContent()
	if !m.scroll.userScrolled {
		m.chatViewport.GotoBottom()
	}
	return m, nil
}

func (m *Model) handleOperatorDone(msg OperatorDoneMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	slog.Debug("operator turn done", "err", msg.Err)
	m.stream.streaming = false
	if msg.Err != nil {
		m.err = msg.Err
		m.updateViewportContent()
		cmds = append(cmds, m.input.Focus())
		return m, tea.Batch(cmds...)
	}
	if !m.stats.ResponseStart.IsZero() {
		m.stats.LastResponseTime = time.Since(m.stats.ResponseStart)
		m.stats.TotalResponseTime += m.stats.LastResponseTime
		m.stats.TotalResponses++
	}
	m.stats.CompletionTokens += msg.TokensOut
	m.stats.ReasoningTokens += msg.ReasoningTokens
	m.stats.CompletionTokensLive = 0
	// PromptTokens tracks the operator's live context occupancy — the prompt
	// size of the most recent round-trip. Only update when the turn reported a
	// value so a zero (e.g. a provider that omits usage) doesn't blank the bar.
	if msg.ContextTokens > 0 {
		m.stats.PromptTokens = msg.ContextTokens
	}
	if m.stream.currentResponse != "" {
		byline := "operator"
		if msg.ModelName != "" {
			byline = "operator · " + msg.ModelName
			m.stats.ModelName = msg.ModelName
		} else if m.stats.ModelName != "" {
			byline = "operator · " + m.stats.ModelName
		}
		m.appendEntry(service.ChatEntry{
			Message: service.ChatMessage{
				Role:    service.MessageRoleAssistant,
				Content: m.stream.currentResponse,
			},
			Timestamp:  time.Now(),
			ClaudeMeta: byline,
		})
		m.stream.currentResponse = ""
	}
	m.updateViewportContent()
	if !m.scroll.userScrolled {
		m.chatViewport.GotoBottom()
	} else {
		m.scroll.hasNewMessages = true
	}
	cmds = append(cmds, m.input.Focus())
	// If the user queued messages while the operator was busy, send
	// the next one automatically.
	if len(m.chat.queuedMessages) > 0 {
		next := m.chat.queuedMessages[0]
		m.chat.queuedMessages = m.chat.queuedMessages[1:]
		m.input.SetValue(next)
		cmds = append(cmds, m.sendMessage())
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleBlockerAdded(msg BlockerAddedMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	b := msg.Blocker
	slog.Debug("blocker added", "request_id", b.RequestID, "questions", len(b.Questions), "source", b.Source)
	// A blocker no longer prompts inline. Queue it, record it in the
	// transcript, and toast — the user answers from the Blockers panel on
	// their own schedule, so a stray Enter can't misfire a response.
	m.flushOperatorStream()
	// Ignore duplicates (e.g. a hydrate that races a live event).
	known := false
	for _, existing := range m.blockers {
		if existing.RequestID == b.RequestID {
			known = true
			break
		}
	}
	if !known {
		m.blockers = append(m.blockers, b)
		m.appendEntry(service.ChatEntry{
			Message: service.ChatMessage{
				Role:    service.MessageRoleAssistant,
				Content: "⛔ Blocker · " + m.blockerLabel(b) + " needs input:\n" + promptHistoryContent(b.Questions),
			},
			Timestamp: time.Now(),
		})
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
		cmds = append(cmds, m.addToast("⛔ Blocker · "+m.blockerLabel(b)+" — "+blockerFirstQuestion(b), toastWarning))
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleOperatorEvent(msg OperatorEventMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	slog.Debug("operator event", "type", msg.Event.Type)
	// All job-scoped events (Job*, Task*) collapse into a single
	// in-place job-update block per job ID. The block mutates as the
	// job progresses and stays at its original chat position.
	dirty := false
	if m.upsertJobUpdateEntry(msg.Event) != nil {
		dirty = true
	}
	// Job completion is also the moment the result block lands —
	// distinct from the in-progress block, sitting *below* it in chat
	// so the conversation history reflects the discrete completion
	// event. Failed/cancelled jobs use the same hook; the renderer
	// branches on Status.
	if msg.Event.Type == service.EventTypeJobCompleted {
		if cmd := m.appendJobResultEntry(msg.Event); cmd != nil {
			cmds = append(cmds, cmd)
		}
		dirty = true
	}
	// A failed task otherwise only nudges the job block's failed-count; the
	// actual reason gets dropped. Surface it as a toast so the operator sees
	// why a node gave up without digging through transcripts.
	if msg.Event.Type == service.EventTypeTaskFailed {
		if p, ok := msg.Event.Payload.(service.TaskFailedPayload); ok {
			short := p.TaskID
			if len(short) > 8 {
				short = short[:8]
			}
			line := "✗ task " + short + " failed"
			if reason := firstLineOf(p.Error); reason != "" {
				line += ": " + truncateStr(reason, 80)
			}
			cmds = append(cmds, m.addToast(line, toastError))
		}
	}
	if dirty {
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleProgressPoll(msg progressPollMsg) (tea.Model, tea.Cmd) {
	m.progress.jobs = msg.Jobs
	m.progress.tasks = msg.Tasks
	m.progress.reports = msg.Progress
	m.progress.activeSessions = msg.Sessions
	m.progress.feedEntries = msg.FeedEntries
	m.progress.mcpServers = msg.MCPServers
	// Rehydrate the Workers panel from the snapshot. Runtime slots are
	// normally created only from live session.*/graph.node_* events, which
	// the SSE stream doesn't replay — so after a reconnect mid-job the panel
	// would be empty even though work is running. Seed any active graph node
	// or live worker session we don't already have a slot for. Idempotent:
	// existing slots are skipped (and just enriched below).
	for _, gn := range msg.GraphNodes {
		if _, ok := m.runtimeSessions[gn.SessionID]; ok {
			continue
		}
		m.runtimeSessions[gn.SessionID] = &runtimeSlot{
			sessionID:  gn.SessionID,
			workerName: "graph:" + gn.Node,
			jobID:      gn.JobID,
			taskID:     gn.TaskID,
			status:     "active",
			startTime:  gn.StartedAt,
			system:     isSystemNode(gn.Node),
		}
		m.recordGraphNodeStarted(gn.JobID, gn.TaskID, gn.Node)
	}
	// Enrich existing slots with cost (and model/provider/token fallbacks) from
	// the persisted session records — the session.* event stream drops these.
	// Runs before the live-snapshot pass so live token/context values win for
	// sessions the runtime is actively streaming (DB token counts only update on
	// completion, so they're stale for in-flight work).
	for _, sess := range msg.Sessions {
		slot, ok := m.runtimeSessions[sess.ID]
		if !ok {
			continue
		}
		slot.model = sess.Model
		slot.provider = sess.Provider
		slot.tokensIn = sess.TokensIn
		slot.tokensOut = sess.TokensOut
		if sess.CostUSD != nil {
			slot.costUSD = *sess.CostUSD
		}
	}
	// Apply live runtime snapshots. These carry real-time token counts and
	// context occupancy, so they create missing slots and authoritatively
	// refresh the live fields of existing ones each tick.
	for _, snap := range msg.LiveSnapshots {
		status := snap.Status
		if status == "" {
			status = "active"
		}
		if slot, ok := m.runtimeSessions[snap.ID]; ok {
			slot.model = snap.Model
			slot.provider = snap.Provider
			slot.tokensIn = snap.TokensIn
			slot.tokensOut = snap.TokensOut
			slot.contextTokens = snap.CurrentContextTokens
			if snap.ContextWindow > 0 {
				slot.ctxWindow = snap.ContextWindow
			}
			continue
		}
		m.runtimeSessions[snap.ID] = &runtimeSlot{
			sessionID:     snap.ID,
			workerName:    snap.WorkerID,
			jobID:         snap.JobID,
			taskID:        snap.TaskID,
			status:        status,
			startTime:     snap.StartTime,
			model:         snap.Model,
			provider:      snap.Provider,
			tokensIn:      snap.TokensIn,
			tokensOut:     snap.TokensOut,
			contextTokens: snap.CurrentContextTokens,
			ctxWindow:     snap.ContextWindow,
		}
	}
	// Reconcile slots whose terminal events were lost during an SSE
	// outage: a slot still marked active but absent from every active
	// set in the snapshot is dead — without this it spins "streaming"
	// forever and the kill flow offers to kill a finished session.
	alive := make(map[string]bool, len(msg.GraphNodes)+len(msg.LiveSnapshots)+len(msg.Sessions))
	for _, gn := range msg.GraphNodes {
		alive[gn.SessionID] = true
	}
	for _, snap := range msg.LiveSnapshots {
		alive[snap.ID] = true
	}
	for _, sess := range msg.Sessions {
		alive[sess.ID] = true
	}
	// The age guard avoids falsely completing a session that started
	// after this snapshot was taken but before the poll was handled.
	for id, slot := range m.runtimeSessions {
		if slot.status == "active" && !alive[id] && time.Since(slot.startTime) > 5*time.Second {
			slot.status = "completed"
			if slot.endTime.IsZero() {
				slot.endTime = time.Now()
			}
		}
	}
	// Keep m.jobs in sync so the Jobs panel (which reads m.jobs via
	// displayJobs) reflects the latest polled state.
	m.jobs = msg.Jobs
	// Re-render any job-update blocks with the fresh state — the
	// discrete JobCompleted / TaskCompleted events race this update,
	// so the blocks have to catch up here to avoid stale status.
	if m.refreshJobUpdateEntries() {
		m.updateViewportContent()
	}
	m.syncJobsModalFromProgress()
	// Kick the spinner ticker if we see animated state but the tick
	// isn't running — handles TUI reconnect mid-job and any other
	// path where active state arrives without sendMessage arming it.
	if !m.spinnerRunning {
		for _, j := range m.displayJobs() {
			if j.Status == service.JobStatusActive || j.Status == service.JobStatusPending {
				m.spinnerRunning = true
				return m, spinnerTick()
			}
		}
	}
	return m, nil
}
