package cmd

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/client"
	"github.com/jefflinse/toasters/internal/server"
	"github.com/jefflinse/toasters/internal/service"
)

// TestEventPayloadRoundTrip pushes a representative payload for every
// service.EventType through the server's wire encoder and the client's
// parser, asserting the client reconstructs exactly what the service
// emitted. A missing case on either side surfaces immediately: the server's
// default branch serializes PascalCase field names the client can't read,
// and the client's default branch returns nil.
//
// When adding a new EventType, add a case here — this test is the contract
// that remote clients see the same world as local subscribers.
func TestEventPayloadRoundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

	cases := map[service.EventType]any{
		service.EventTypeOperatorText: service.OperatorTextPayload{
			Text: "hello", Reasoning: "thinking",
		},
		service.EventTypeOperatorToolCall: service.OperatorToolCallPayload{
			Name: "create_job", Args: json.RawMessage(`{"title":"x"}`), Result: "ok", IsError: false,
		},
		service.EventTypeOperatorDone: service.OperatorDonePayload{
			ModelName: "m", TokensIn: 10, TokensOut: 20, ReasoningTokens: 5,
		},
		service.EventTypeBlockerAdded: service.Blocker{
			RequestID: "req-1", Source: "graph:investigate", JobID: "j-1", TaskID: "t-1",
			Questions: []service.PromptQuestion{{Question: "Q?", Options: []string{"a", "b"}}},
			CreatedAt: ts,
		},
		service.EventTypeBlockerResolved: service.BlockerResolvedPayload{RequestID: "req-1"},
		service.EventTypeJobCreated: service.JobCreatedPayload{
			JobID: "j-1", Title: "T", Description: "D",
		},
		service.EventTypeTaskCreated: service.TaskCreatedPayload{
			TaskID: "t-1", JobID: "j-1", Title: "T", GraphID: "g-1",
		},
		service.EventTypeTaskAssigned: service.TaskAssignedPayload{
			TaskID: "t-1", JobID: "j-1", GraphID: "g-1", Title: "T",
		},
		service.EventTypeTaskStarted: service.TaskStartedPayload{
			TaskID: "t-1", JobID: "j-1", GraphID: "g-1", Title: "T",
		},
		service.EventTypeTaskCompleted: service.TaskCompletedPayload{
			TaskID: "t-1", JobID: "j-1", GraphID: "g-1", Summary: "done",
			Recommendations: "next", HasNextTask: true,
		},
		service.EventTypeTaskFailed: service.TaskFailedPayload{
			TaskID: "t-1", JobID: "j-1", GraphID: "g-1", Error: "boom",
		},
		service.EventTypeJobCompleted: service.JobCompletedPayload{
			JobID: "j-1", Title: "T", Summary: "S", Status: service.JobStatusCompleted,
			Workspace: "/ws", StartedAt: ts, EndedAt: ts.Add(time.Minute),
			TasksTotal: 3, TasksCompleted: 2, TasksFailed: 1,
			TokensIn: 100, TokensOut: 200, CostUSD: 0.5,
			FilesTouched:      []service.FileTouch{{Path: "a.go", Size: 42, IsNew: true}},
			FilesTouchedExtra: 7,
		},
		service.EventTypeSessionStarted: service.SessionStartedPayload{
			SessionID: "s-1", WorkerName: "w", Task: "do it", JobID: "j-1", TaskID: "t-1",
			SystemPrompt: "sp", InitialMessage: "im",
		},
		service.EventTypeSessionText:      service.SessionTextPayload{Text: "tok"},
		service.EventTypeSessionReasoning: service.SessionReasoningPayload{Text: "cot"},
		service.EventTypeSessionToolCall: service.SessionToolCallPayload{
			ToolCall: service.ToolCall{ID: "c-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"x"}`)},
		},
		service.EventTypeSessionToolResult: service.SessionToolResultPayload{
			Result: service.ToolCallResult{CallID: "c-1", Name: "read_file", Result: "data", Error: "e"},
		},
		service.EventTypeSessionFileChange: service.SessionFileChangePayload{
			ToolName: "edit_file", Path: "a.go", Diff: "@@ -1 +1 @@\n-old\n+new\n",
			Added: 1, Removed: 1, Created: true, Truncated: true,
		},
		service.EventTypeSessionShellExec: service.SessionShellExecPayload{
			Command: "go test ./...", ExitCode: 1, DurationMs: 1234, OutputBytes: 512,
			Truncated: true, TimedOut: false,
		},
		service.EventTypeSessionDone: service.SessionDonePayload{
			WorkerName: "w", JobID: "j-1", TaskID: "t-1", Status: "completed", FinalText: "bye",
		},
		service.EventTypeSessionMeta: service.SessionMetaPayload{
			SessionID: "s-1", Model: "m", Provider: "p", Temperature: 0.7, Thinking: true,
		},
		service.EventTypeSessionPrompt: service.SessionPromptPayload{
			SessionID: "s-1", SystemPrompt: "sp", InitialMessage: "im",
		},
		service.EventTypeOperationCompleted: service.OperationCompletedPayload{
			Kind: "generate_skill",
			Result: service.OperationResult{
				OperationID: "op-1", Content: "content", Error: "",
			},
		},
		service.EventTypeOperationFailed: service.OperationFailedPayload{
			Kind: "generate_skill", Error: "boom",
		},
		service.EventTypeHeartbeat:      service.HeartbeatPayload{ServerTime: ts},
		service.EventTypeConnectionLost: service.ConnectionLostPayload{Error: "gone"},
		// connection.restored carries an empty payload struct.
		service.EventTypeConnectionRestored: service.ConnectionRestoredPayload{},
		service.EventTypeGraphNodeStarted: service.GraphNodeStartedPayload{
			JobID: "j-1", TaskID: "t-1", Node: "investigate",
		},
		service.EventTypeGraphNodeCompleted: service.GraphNodeCompletedPayload{
			JobID: "j-1", TaskID: "t-1", Node: "investigate", Status: "failed",
		},
		service.EventTypeGraphCompleted: service.GraphCompletedPayload{
			JobID: "j-1", TaskID: "t-1", Summary: "done",
		},
		service.EventTypeGraphFailed: service.GraphFailedPayload{
			JobID: "j-1", TaskID: "t-1", Error: "boom",
		},
	}

	for et, payload := range cases {
		t.Run(string(et), func(t *testing.T) {
			wire := server.EventPayloadToWire(service.Event{Type: et, Payload: payload})
			data, err := json.Marshal(wire)
			if err != nil {
				t.Fatalf("marshaling wire payload: %v", err)
			}

			got, err := client.ParseSSEPayload(string(et), data)
			if err != nil {
				t.Fatalf("parsing wire payload: %v", err)
			}
			if got == nil {
				t.Fatalf("client returned nil payload — missing ParseSSEPayload case for %s (wire JSON: %s)", et, data)
			}
			if !reflect.DeepEqual(got, payload) {
				t.Errorf("round-trip mismatch for %s:\n  sent: %#v\n  got:  %#v\n  wire: %s", et, payload, got, data)
			}
		})
	}

	// definitions.reloaded carries no payload; the parser must return nil
	// without error.
	t.Run(string(service.EventTypeDefinitionsReloaded), func(t *testing.T) {
		got, err := client.ParseSSEPayload(string(service.EventTypeDefinitionsReloaded), nil)
		if err != nil {
			t.Fatalf("parsing nil payload: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil payload, got %#v", got)
		}
	})

	// progress.update is exercised separately: ProgressState is a deep
	// composite, so use a populated instance to verify the nested wire
	// conversions.
	t.Run(string(service.EventTypeProgressUpdate), func(t *testing.T) {
		payload := service.ProgressUpdatePayload{State: service.ProgressState{
			Jobs: []service.Job{{
				ID: "j-1", Title: "T", Description: "D", Type: "test",
				Status: service.JobStatusActive, CreatedAt: ts, UpdatedAt: ts,
			}},
			Tasks: map[string][]service.Task{
				"j-1": {{
					ID: "t-1", JobID: "j-1", Title: "T", Status: service.TaskStatusInProgress,
					CreatedAt: ts, UpdatedAt: ts,
				}},
			},
			Reports: map[string][]service.ProgressReport{
				"j-1": {{ID: 1, JobID: "j-1", TaskID: "t-1", WorkerID: "w", Status: "working", Message: "m", CreatedAt: ts}},
			},
		}}

		wire := server.EventPayloadToWire(service.Event{Type: service.EventTypeProgressUpdate, Payload: payload})
		data, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshaling wire payload: %v", err)
		}
		got, err := client.ParseSSEPayload(string(service.EventTypeProgressUpdate), data)
		if err != nil {
			t.Fatalf("parsing wire payload: %v", err)
		}
		gotPayload, ok := got.(service.ProgressUpdatePayload)
		if !ok {
			t.Fatalf("got %T, want service.ProgressUpdatePayload", got)
		}
		if len(gotPayload.State.Jobs) != 1 || gotPayload.State.Jobs[0].ID != "j-1" {
			t.Errorf("jobs did not round-trip: %#v", gotPayload.State.Jobs)
		}
		if len(gotPayload.State.Tasks["j-1"]) != 1 || gotPayload.State.Tasks["j-1"][0].ID != "t-1" {
			t.Errorf("tasks did not round-trip: %#v", gotPayload.State.Tasks)
		}
		if len(gotPayload.State.Reports["j-1"]) != 1 || gotPayload.State.Reports["j-1"][0].Message != "m" {
			t.Errorf("reports did not round-trip: %#v", gotPayload.State.Reports)
		}
	})
}
