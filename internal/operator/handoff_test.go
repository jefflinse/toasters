package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// fixedWindow is a ContextWindowSource returning one value for everything.
type fixedWindow int

func (w fixedWindow) Window(_, _ string) int { return int(w) }

// bigTurnResponse is a mock response whose usage reports occupancy well over
// a 50% threshold against a 1000-token window.
func bigTurnResponse(text string) mockResponse {
	return mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventText, Text: text},
		{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: 900, OutputTokens: 10}},
		{Type: provider.EventDone},
	}}
}

// newHandoffOperator builds an operator wired for compaction tests: 1000-token
// window, threshold 50%, session file in a temp dir, compaction events
// captured. Extra config tweaks apply via mutate.
func newHandoffOperator(t *testing.T, mp *mockProvider, mutate func(*Config)) (*Operator, func() []CompactionPayload) {
	t.Helper()
	var mu sync.Mutex
	var compactions []CompactionPayload
	cfg := Config{
		Runtime:             runtime.New(nil, newTestRegistry(mp)),
		Provider:            mp,
		Model:               "test-model",
		WorkDir:             t.TempDir(),
		SystemPrompt:        "You are the operator.",
		SessionFile:         filepath.Join(t.TempDir(), "sessions", "operator.json"),
		ContextWindows:      fixedWindow(1000),
		CompactionThreshold: 50,
		OnEvent: func(ev Event) {
			if ev.Type != EventCompaction {
				return
			}
			if p, ok := ev.Payload.(CompactionPayload); ok {
				mu.Lock()
				compactions = append(compactions, p)
				mu.Unlock()
			}
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	op, err := New(cfg)
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}
	return op, func() []CompactionPayload {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]CompactionPayload, len(compactions))
		copy(cp, compactions)
		return cp
	}
}

func sendUserTurn(t *testing.T, ctx context.Context, op *Operator, text string) {
	t.Helper()
	if err := op.Send(ctx, Event{Type: EventUserMessage, Payload: UserMessagePayload{Text: text}}); err != nil {
		t.Fatalf("sending user message: %v", err)
	}
}

func TestHandoff_TriggersAtThreshold(t *testing.T) {
	mp := &mockProvider{name: "test", responses: []mockResponse{
		bigTurnResponse("working on it"),
		bigTurnResponse("second turn"),
	}}
	op, compactions := newHandoffOperator(t, mp, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op.Start(ctx)
	sendUserTurn(t, ctx, op, "do a thing")

	// The turn reports 900/1000 tokens (>= 50%), so the post-event check
	// must compact: one handoff message replaces the history.
	waitFor(t, func() bool { return len(compactions()) == 1 }, 2*time.Second)

	op.mu.Lock()
	msgCount := len(op.messages)
	content := ""
	if msgCount > 0 {
		content = op.messages[0].Content
	}
	op.mu.Unlock()
	if msgCount != 1 {
		t.Fatalf("messages after handoff = %d, want 1", msgCount)
	}
	assertContains(t, content, "Operator handoff")
	assertContains(t, content, "Orchestration state")

	ev := compactions()[0]
	if ev.BeforeTokens != 900 {
		t.Errorf("BeforeTokens = %d, want 900", ev.BeforeTokens)
	}
	if ev.EstimatedAfterTokens <= 0 || ev.EstimatedAfterTokens >= 900 {
		t.Errorf("EstimatedAfterTokens = %d, want small positive estimate", ev.EstimatedAfterTokens)
	}
	if ev.ArchiveFile == "" || !strings.HasPrefix(ev.ArchiveFile, "operator-") {
		t.Errorf("ArchiveFile = %q, want operator-<timestamp>.json basename", ev.ArchiveFile)
	}

	// The archive holds the pre-handoff history.
	archivePath := filepath.Join(filepath.Dir(op.sessionFile), "archive", ev.ArchiveFile)
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("reading archive: %v", err)
	}
	var archived operatorSession
	if err := json.Unmarshal(data, &archived); err != nil {
		t.Fatalf("parsing archive: %v", err)
	}
	if len(archived.Messages) < 2 {
		t.Errorf("archived messages = %d, want the full pre-handoff history", len(archived.Messages))
	}

	// The next turn's request must open from the handoff message.
	sendUserTurn(t, ctx, op, "next thing")
	waitFor(t, func() bool { return len(mp.getRequests()) == 2 }, 2*time.Second)
	req := mp.getRequests()[1]
	if len(req.Messages) == 0 || !strings.Contains(req.Messages[0].Content, "Operator handoff") {
		t.Errorf("second request does not start from the handoff message")
	}
}

func TestHandoff_IncludesNarrativeWhenRoleAvailable(t *testing.T) {
	// Response order: the user turn, then the narrative one-shot.
	mp := &mockProvider{name: "test", responses: []mockResponse{
		bigTurnResponse("working"),
		{events: []provider.StreamEvent{
			{Type: provider.EventText, Text: "The user wants the frobnicator refactored; I was about to split it into a job."},
			{Type: provider.EventDone},
		}},
	}}
	engine := testPromptEngine(t, map[string]string{
		"operator-handoff": "Write a short handoff note.",
	})
	op, compactions := newHandoffOperator(t, mp, func(cfg *Config) {
		cfg.PromptEngine = engine
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op.Start(ctx)
	sendUserTurn(t, ctx, op, "refactor the frobnicator")
	waitFor(t, func() bool { return len(compactions()) == 1 }, 2*time.Second)

	op.mu.Lock()
	content := op.messages[0].Content
	op.mu.Unlock()
	assertContains(t, content, "Handoff note from the previous session")
	assertContains(t, content, "frobnicator refactored")

	// The narrative call must be a bounded one-shot on the operator's model,
	// with the stripped transcript — not the raw history.
	reqs := mp.getRequests()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2 (turn + narrative)", len(reqs))
	}
	nreq := reqs[1]
	if nreq.MaxTokens != narrativeMaxTokens {
		t.Errorf("narrative MaxTokens = %d, want %d", nreq.MaxTokens, narrativeMaxTokens)
	}
	if nreq.Model != "test-model" {
		t.Errorf("narrative Model = %q, want test-model", nreq.Model)
	}
	if len(nreq.Tools) != 0 {
		t.Errorf("narrative call has %d tools, want 0 (stateless one-shot)", len(nreq.Tools))
	}
}

func TestHandoff_NarrativeFailureDegradesToDigestOnly(t *testing.T) {
	mp := &mockProvider{name: "test", responses: []mockResponse{
		bigTurnResponse("working"),
		{err: fmt.Errorf("provider exploded")},
	}}
	engine := testPromptEngine(t, map[string]string{
		"operator-handoff": "Write a short handoff note.",
	})
	op, compactions := newHandoffOperator(t, mp, func(cfg *Config) {
		cfg.PromptEngine = engine
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op.Start(ctx)
	sendUserTurn(t, ctx, op, "do a thing")
	waitFor(t, func() bool { return len(compactions()) == 1 }, 2*time.Second)

	op.mu.Lock()
	content := op.messages[0].Content
	op.mu.Unlock()
	assertContains(t, content, "Orchestration state")
	if strings.Contains(content, "Handoff note") {
		t.Errorf("handoff should be digest-only when the narrative call fails")
	}
}

func TestHandoff_DisabledCases(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"threshold zero", func(cfg *Config) { cfg.CompactionThreshold = 0 }},
		{"window unknown", func(cfg *Config) { cfg.ContextWindows = fixedWindow(0) }},
		{"no resolver", func(cfg *Config) { cfg.ContextWindows = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mp := &mockProvider{name: "test", responses: []mockResponse{
				bigTurnResponse("working"),
			}}
			op, compactions := newHandoffOperator(t, mp, tc.mutate)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			op.Start(ctx)
			sendUserTurn(t, ctx, op, "do a thing")
			waitFor(t, func() bool { return len(mp.getRequests()) == 1 }, 2*time.Second)
			// Give the post-event check a beat to (not) fire.
			time.Sleep(50 * time.Millisecond)

			if n := len(compactions()); n != 0 {
				t.Errorf("compactions = %d, want 0", n)
			}
			op.mu.Lock()
			msgCount := len(op.messages)
			op.mu.Unlock()
			if msgCount < 2 {
				t.Errorf("messages = %d, want the un-compacted history", msgCount)
			}
		})
	}
}

func TestHandoff_BelowThresholdDoesNotTrigger(t *testing.T) {
	mp := &mockProvider{name: "test", responses: []mockResponse{
		{events: []provider.StreamEvent{
			{Type: provider.EventText, Text: "small turn"},
			{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: 400, OutputTokens: 5}},
			{Type: provider.EventDone},
		}},
	}}
	op, compactions := newHandoffOperator(t, mp, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op.Start(ctx)
	sendUserTurn(t, ctx, op, "small thing")
	waitFor(t, func() bool { return len(mp.getRequests()) == 1 }, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	if n := len(compactions()); n != 0 {
		t.Errorf("compactions = %d, want 0 at 400/1000 against a 50%% threshold", n)
	}
}

func TestHandoff_RestoredSessionOverThresholdCompactsAtStart(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "sessions", "operator.json")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatal(err)
	}
	sess := operatorSession{
		Messages: []operatorMessage{
			{Role: "user", Content: "old request"},
			{Role: "assistant", Content: "old reply"},
		},
		ContextTokens: 900,
		UpdatedAt:     time.Now(),
	}
	data, _ := json.Marshal(sess)
	if err := os.WriteFile(sessionFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	mp := &mockProvider{name: "test"}
	op, compactions := newHandoffOperator(t, mp, func(cfg *Config) {
		cfg.SessionFile = sessionFile
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op.Start(ctx)

	// No events needed: the restore-time check fires on loop start.
	waitFor(t, func() bool { return len(compactions()) == 1 }, 2*time.Second)
	if got := compactions()[0].BeforeTokens; got != 900 {
		t.Errorf("BeforeTokens = %d, want the restored context_tokens 900", got)
	}
}

func TestHandoff_ArchiveFailureAborts(t *testing.T) {
	mp := &mockProvider{name: "test", responses: []mockResponse{
		bigTurnResponse("working"),
	}}
	sessionDir := t.TempDir()
	// Occupy the archive path with a FILE so MkdirAll fails.
	if err := os.WriteFile(filepath.Join(sessionDir, "archive"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	op, compactions := newHandoffOperator(t, mp, func(cfg *Config) {
		cfg.SessionFile = filepath.Join(sessionDir, "operator.json")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op.Start(ctx)
	sendUserTurn(t, ctx, op, "do a thing")
	waitFor(t, func() bool { return len(mp.getRequests()) == 1 }, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	if n := len(compactions()); n != 0 {
		t.Errorf("compactions = %d, want 0 when archiving fails", n)
	}
	op.mu.Lock()
	msgCount := len(op.messages)
	op.mu.Unlock()
	if msgCount < 2 {
		t.Errorf("messages = %d, want history preserved when archiving fails", msgCount)
	}
}

func TestPruneArchives(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i := range 25 {
		name := fmt.Sprintf("operator-2026-01-%02dT00-00-00Z.json", i+1)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A non-archive file must be untouched.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	pruneArchives(dir, 20)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var archives []string
	keptNotes := false
	for _, e := range entries {
		if e.Name() == "notes.txt" {
			keptNotes = true
			continue
		}
		archives = append(archives, e.Name())
	}
	if len(archives) != 20 {
		t.Errorf("archives after prune = %d, want 20", len(archives))
	}
	if !keptNotes {
		t.Error("prune removed an unrelated file")
	}
	// The oldest five (days 01–05) must be the ones gone.
	for _, a := range archives {
		if a <= "operator-2026-01-05T00-00-00Z.json" {
			t.Errorf("old archive %s survived prune", a)
		}
	}
}

func TestStripTranscript(t *testing.T) {
	t.Parallel()

	msgs := []provider.Message{
		{Role: "user", Content: "build me a thing"},
		{Role: "assistant", Content: "on it", ToolCalls: []provider.ToolCall{{Name: "create_job"}}},
		{Role: "tool", Content: `{"job_id":"j-1","secret":"tool result contents"}`, ToolCallID: "tc-1"},
		{Role: "assistant", Content: "created the job"},
	}
	got := stripTranscript(msgs, 10)

	assertContains(t, got, "USER: build me a thing")
	assertContains(t, got, "[called create_job]")
	assertContains(t, got, "OPERATOR: created the job")
	if strings.Contains(got, "tool result contents") {
		t.Errorf("tool result content leaked into stripped transcript:\n%s", got)
	}

	// The cap keeps only the most recent messages.
	var long []provider.Message
	for i := range 50 {
		long = append(long, provider.Message{Role: "user", Content: fmt.Sprintf("msg-%d", i)})
	}
	capped := stripTranscript(long, 10)
	if strings.Contains(capped, "msg-39") || !strings.Contains(capped, "msg-40") {
		t.Errorf("cap kept the wrong window:\n%s", capped)
	}
}

func TestSessionFile_RoundTripsContextTokens(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "operator.json")
	mp := &mockProvider{name: "test"}
	op, _ := newHandoffOperator(t, mp, func(cfg *Config) {
		cfg.SessionFile = sessionFile
		cfg.CompactionThreshold = 0 // not testing the trigger here
	})

	op.mu.Lock()
	op.messages = []provider.Message{{Role: "user", Content: "hello"}}
	op.lastContextTokens = 4321
	op.mu.Unlock()
	op.persistSession()

	restored, _ := newHandoffOperator(t, &mockProvider{name: "test"}, func(cfg *Config) {
		cfg.SessionFile = sessionFile
		cfg.CompactionThreshold = 0
	})
	restored.mu.Lock()
	got := restored.lastContextTokens
	restored.mu.Unlock()
	if got != 4321 {
		t.Errorf("restored lastContextTokens = %d, want 4321", got)
	}
}

func TestLoadSession_EstimatesTokensForOldFormat(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "operator.json")
	content := strings.Repeat("x", 4000)
	sess := operatorSession{
		Messages:  []operatorMessage{{Role: "user", Content: content}},
		UpdatedAt: time.Now(),
		// No ContextTokens — pre-handoff file format.
	}
	data, _ := json.Marshal(sess)
	if err := os.WriteFile(sessionFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	op, _ := newHandoffOperator(t, &mockProvider{name: "test"}, func(cfg *Config) {
		cfg.SessionFile = sessionFile
		cfg.CompactionThreshold = 0
	})
	op.mu.Lock()
	got := op.lastContextTokens
	op.mu.Unlock()
	if got != 1000 {
		t.Errorf("estimated tokens = %d, want 1000 (4000 bytes / 4)", got)
	}
}

func TestBuildDigest(t *testing.T) {
	t.Parallel()

	store := newOperatorTestStore(t)
	ctx := context.Background()

	job := &db.Job{ID: "job-1", Title: "Refactor frobnicator", Description: "Split it\ninto parts", Status: db.JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &db.Task{ID: "task-1", JobID: "job-1", Title: "Plan the split", Status: db.TaskStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &db.Task{ID: "task-2", JobID: "job-1", Title: "Do the split", Status: db.TaskStatusFailed, Summary: "worker hit a\ncompile error"}); err != nil {
		t.Fatal(err)
	}
	doneJob := &db.Job{ID: "job-0", Title: "Earlier job", Status: db.JobStatusCompleted}
	if err := store.CreateJob(ctx, doneJob); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	if err := store.CreateBlocker(ctx, &db.BlockerRecord{
		RequestID: "req-1",
		Source:    "graph:review",
		JobID:     "job-1",
		Questions: `[{"question":"Ship it anyway?"}]`,
		CreatedAt: fixed.Add(-90 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	got := buildDigest(ctx, store, func() time.Time { return fixed })

	for _, want := range []string{
		"### Jobs in flight",
		"**Refactor frobnicator** (`job-1`, active): Split it into parts",
		"[completed] Plan the split (`task-1`)",
		"[failed] Do the split (`task-2`) — worker hit a compile error",
		"### Pending questions to the user",
		"[graph:review, waiting 1h30m0s] Ship it anyway?",
		"### Recently finished jobs",
		"**Earlier job** (`job-0`, completed)",
	} {
		assertContains(t, got, want)
	}
}

func TestBuildDigest_NilStore(t *testing.T) {
	t.Parallel()
	got := buildDigest(context.Background(), nil, time.Now)
	assertContains(t, got, "state store unavailable")
}
