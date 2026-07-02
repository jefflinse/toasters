package tui

import (
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

// TestTranslateEvent_OperatorCompaction verifies the service event becomes
// the Bubble Tea message the fleet row trace consumes.
func TestTranslateEvent_OperatorCompaction(t *testing.T) {
	t.Parallel()

	msg := translateEvent(service.Event{
		Type: service.EventTypeOperatorCompaction,
		Payload: service.OperatorCompactionPayload{
			BeforeTokens:         5200,
			EstimatedAfterTokens: 1800,
			ArchiveFile:          "operator-x.json",
		},
	})
	got, ok := msg.(OperatorCompactionMsg)
	if !ok {
		t.Fatalf("translated msg = %T, want OperatorCompactionMsg", msg)
	}
	if got.BeforeTokens != 5200 || got.EstimatedAfterTokens != 1800 || got.ArchiveFile != "operator-x.json" {
		t.Errorf("msg = %+v", got)
	}

	// A mismatched payload must not panic — it drops the event.
	if m := translateEvent(service.Event{Type: service.EventTypeOperatorCompaction, Payload: "junk"}); m != nil {
		t.Errorf("bad payload translated to %T, want nil", m)
	}
}

// TestTranslateEvent_SessionCompaction verifies the worker compaction event
// becomes the Bubble Tea message the fleet row trace consumes.
func TestTranslateEvent_SessionCompaction(t *testing.T) {
	t.Parallel()

	msg := translateEvent(service.Event{
		Type: service.EventTypeSessionCompaction,
		Payload: service.SessionCompactionPayload{
			SessionID:            "sess-1",
			Tier:                 2,
			BeforeTokens:         41000,
			EstimatedAfterTokens: 9000,
		},
	})
	got, ok := msg.(SessionCompactionMsg)
	if !ok {
		t.Fatalf("translated msg = %T, want SessionCompactionMsg", msg)
	}
	if got.SessionID != "sess-1" || got.Tier != 2 || got.BeforeTokens != 41000 || got.EstimatedAfterTokens != 9000 {
		t.Errorf("msg = %+v", got)
	}
	if m := translateEvent(service.Event{Type: service.EventTypeSessionCompaction, Payload: "junk"}); m != nil {
		t.Errorf("bad payload translated to %T, want nil", m)
	}
}
