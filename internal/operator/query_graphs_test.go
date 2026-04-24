package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/graphexec"
)

// stubCatalog implements GraphCatalog for tests.
type stubCatalog struct{ graphs []*graphexec.Definition }

func (s stubCatalog) Graphs() []*graphexec.Definition { return s.graphs }

func newSystemToolsWithCatalog(t *testing.T, graphs []*graphexec.Definition) *SystemTools {
	t.Helper()

	workDir := t.TempDir()
	t.Setenv("HOME", workDir)
	return NewSystemTools(SystemToolsConfig{
		DefaultProvider: "mock",
		DefaultModel:    "m",
		EventCh:         make(chan Event, 8),
		WorkDir:         workDir,
		GraphCatalog:    stubCatalog{graphs: graphs},
	})
}

func TestQueryGraphs_NoCatalog(t *testing.T) {
	st := NewSystemTools(SystemToolsConfig{
		DefaultProvider: "mock",
		DefaultModel:    "m",
		EventCh:         make(chan Event, 8),
		WorkDir:         t.TempDir(),
	})
	result, err := st.Execute(context.Background(), "query_graphs", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "No graphs") {
		t.Errorf("expected 'No graphs' message, got %q", result)
	}
}

func TestQueryGraphs_EmptyCatalog(t *testing.T) {
	st := newSystemToolsWithCatalog(t, nil)
	result, err := st.Execute(context.Background(), "query_graphs", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "No graphs") {
		t.Errorf("expected 'No graphs' message, got %q", result)
	}
}

func TestQueryGraphs_ListsGraphs(t *testing.T) {
	st := newSystemToolsWithCatalog(t, []*graphexec.Definition{
		{
			ID:          "bug-fix",
			Name:        "Bug Fix",
			Description: "Investigate, plan, implement, test, review",
			Tags:        []string{"kind:bugfix", "language:go"},
		},
		{
			ID:   "prototype",
			Name: "Prototype",
			// no description / no tags
		},
	})

	result, err := st.Execute(context.Background(), "query_graphs", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, want := range []string{
		"bug-fix", "Bug Fix", "Investigate, plan",
		"kind:bugfix", "language:go",
		"prototype", "Prototype",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("expected %q in output:\n%s", want, result)
		}
	}
}

func TestQueryGraphs_IsListedInDefinitions(t *testing.T) {
	st := newSystemToolsWithCatalog(t, nil)
	found := false
	for _, td := range st.Definitions() {
		if td.Name == "query_graphs" {
			found = true
			break
		}
	}
	if !found {
		t.Error("query_graphs missing from SystemTools.Definitions()")
	}
}
