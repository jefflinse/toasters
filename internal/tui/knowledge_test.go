package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// renderKnowledgeSmoke renders the Knowledge screen for the given model and
// fails the test if it panics or produces an empty string, across the given
// terminal widths/heights. It exists so every state transition below gets
// the same "doesn't blow up the slice math" check.
func renderKnowledgeSmoke(t *testing.T, m *Model, dims ...[2]int) {
	t.Helper()
	if len(dims) == 0 {
		dims = [][2]int{{100, 40}}
	}
	for _, d := range dims {
		m.width, m.height = d[0], d[1]
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("renderKnowledge panicked at %dx%d: %v", d[0], d[1], r)
				}
			}()
			out := m.renderKnowledge()
			if out == "" {
				t.Errorf("renderKnowledge at %dx%d returned empty string", d[0], d[1])
			}
		}()
	}
}

// narrowDims exercises a very narrow terminal, where knowledgeLayoutFor's
// clamps (listInnerW/detailInnerW/visibleRows) are most likely to go
// negative or zero if the arithmetic has an off-by-one.
var narrowDims = [][2]int{{100, 40}, {24, 10}}

func TestRenderKnowledge_NoJobSelected(t *testing.T) {
	m := newMinimalModel(t)
	m.knowledge.show = true
	m.knowledge.jobID = ""

	renderKnowledgeSmoke(t, &m, narrowDims...)
}

func TestRenderKnowledge_Loading(t *testing.T) {
	m := newMinimalModel(t)
	m.knowledge.show = true
	m.knowledge.jobID = "job-1"
	m.knowledge.loading = true

	renderKnowledgeSmoke(t, &m, narrowDims...)
}

func TestRenderKnowledge_Error(t *testing.T) {
	m := newMinimalModel(t)
	m.knowledge.show = true
	m.knowledge.jobID = "job-1"
	m.knowledge.err = errors.New("boom")

	renderKnowledgeSmoke(t, &m, narrowDims...)
}

func TestRenderKnowledge_EmptyNotes(t *testing.T) {
	m := newMinimalModel(t)
	m.knowledge.show = true
	m.knowledge.jobID = "job-1"
	m.knowledge.notes = nil

	renderKnowledgeSmoke(t, &m, narrowDims...)
}

func TestRenderKnowledge_PopulatedListFocused(t *testing.T) {
	m := newMinimalModel(t)
	m.knowledge.show = true
	m.knowledge.jobID = "job-1"
	m.knowledge.notes = []service.NoteMeta{
		{ID: "20260101-090000.000-backend-worker-fixed-bug-abc123", Title: "Fixed the bug", Source: "backend", ModTime: time.Now().Add(-time.Hour), Size: 128},
		{ID: "20260102-100000.000-frontend-worker-added-tests-def456", Title: "Added tests", Source: "frontend", ModTime: time.Now(), Size: 256},
	}
	m.knowledge.selected = 1
	m.knowledge.focusDetail = false

	renderKnowledgeSmoke(t, &m, narrowDims...)
}

func TestRenderKnowledge_PopulatedDetailFocusedScrollBottom(t *testing.T) {
	m := newMinimalModel(t)
	m.knowledge.show = true
	m.knowledge.jobID = "job-1"
	m.knowledge.notes = []service.NoteMeta{
		{ID: "20260101-090000.000-backend-worker-fixed-bug-abc123", Title: "Fixed the bug", Source: "backend", ModTime: time.Now(), Size: 128},
	}
	m.knowledge.selected = 0
	m.knowledge.focusDetail = true
	m.knowledge.content = "# Fixed the bug\n\nRoot cause was a race condition.\nLine 3.\nLine 4.\nLine 5."
	m.knowledge.contentScroll = scrollBottom

	renderKnowledgeSmoke(t, &m, narrowDims...)

	// scrollBottom must have been clamped to a real offset, not left as the
	// 1<<30 sentinel — renderKnowledge writes the clamped value back.
	if m.knowledge.contentScroll == scrollBottom {
		t.Errorf("contentScroll left at sentinel scrollBottom, want clamped")
	}
}

// TestOpenKnowledge_NoJobsShowsEmptyState verifies openKnowledge opens the
// screen with an empty jobID (rather than not opening at all) when there
// are no jobs to select from, per the "if no jobs, show=true with an
// empty-state" requirement.
func TestOpenKnowledge_NoJobsShowsEmptyState(t *testing.T) {
	m := newMinimalModel(t)
	m.jobs = nil

	cmd := m.openKnowledge()
	if !m.knowledge.show {
		t.Error("openKnowledge should set show = true even with no jobs")
	}
	if m.knowledge.jobID != "" {
		t.Errorf("jobID = %q, want empty when no jobs exist", m.knowledge.jobID)
	}
	if cmd != nil {
		t.Error("openKnowledge should return a nil cmd when there's no job to fetch notes for")
	}
}

// TestToggleKnowledge_TogglesShow verifies the open/close toggle behavior
// mirrors toggleNodes.
func TestToggleKnowledge_TogglesShow(t *testing.T) {
	m := newMinimalModel(t)
	m.jobs = nil

	_ = m.toggleKnowledge()
	if !m.knowledge.show {
		t.Fatal("first toggle should open the screen")
	}
	cmd := m.toggleKnowledge()
	if m.knowledge.show {
		t.Fatal("second toggle should close the screen")
	}
	if cmd != nil {
		t.Error("closing toggle should return a nil cmd")
	}
}
