package graphexec

import (
	"strings"
	"testing"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		expr      string
		wantNode  string
		wantField string
		wantOK    bool
	}{
		{"$test.output.passed", "test", "passed", true},
		{"$review.output.approved", "review", "approved", true},
		{"$triage.output.diagnosis.severity", "triage", "diagnosis.severity", true},
		{"$node-1.output.field_a", "node-1", "field_a", true},
		{"test.output.passed", "", "", false},   // missing $
		{"$test.passed", "", "", false},         // missing .output
		{"$test.output", "", "", false},         // no field
		{"$test.input.passed", "", "", false},   // wrong segment
		{"", "", "", false},                     // empty
		{"$test.output.foo bar", "", "", false}, // whitespace not allowed
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			node, field, ok := parsePath(tc.expr)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if node != tc.wantNode {
				t.Errorf("node = %q, want %q", node, tc.wantNode)
			}
			if field != tc.wantField {
				t.Errorf("field = %q, want %q", field, tc.wantField)
			}
		})
	}
}

// validDef is a minimal well-formed Definition used as a starting point for
// negative-test permutations.
func validDef() *Definition {
	return &Definition{
		ID:    "g",
		Entry: "a",
		Nodes: []Node{
			{ID: "a", Role: "investigator"},
			{ID: "b", Role: "planner"},
		},
		Edges: []Edge{
			{From: "a", To: "b"},
			{From: "b", To: EndNode},
		},
	}
}

func TestValidate_AcceptsMinimalGraph(t *testing.T) {
	if err := validDef().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_AcceptsConditionalRouter(t *testing.T) {
	def := &Definition{
		ID:    "g",
		Entry: "t",
		Nodes: []Node{
			{ID: "t", Role: "tester"},
			{ID: "i", Role: "implementer"},
		},
		Edges: []Edge{
			{From: "t", Router: &Router{
				On: "$t.output.passed",
				Branches: []Branch{
					{When: true, To: EndNode},
					{When: false, To: "i"},
				},
			}},
			{From: "i", To: "t"},
		},
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*Definition)
		contains string
	}{
		{"empty id", func(d *Definition) { d.ID = "" }, "id is required"},
		{"no nodes", func(d *Definition) { d.Nodes = nil }, "has no nodes"},
		{"no entry", func(d *Definition) { d.Entry = "" }, "has no entry"},
		{"unknown entry", func(d *Definition) { d.Entry = "zzz" }, "entry"},
		{"duplicate node id", func(d *Definition) {
			d.Nodes = append(d.Nodes, Node{ID: "a", Role: "investigator"})
		}, "duplicate node id"},
		{"node id collides with end sentinel", func(d *Definition) {
			d.Nodes[0].ID = EndNode
			d.Entry = EndNode
		}, "end sentinel"},
		{"node missing role", func(d *Definition) { d.Nodes[0].Role = "" }, "must set role or graph"},
		{"node sets both role and graph", func(d *Definition) {
			d.Nodes[0].Graph = "other"
		}, "both role and graph"},
		{"edge unknown from", func(d *Definition) {
			d.Edges[0].From = "nope"
		}, "from"},
		{"edge unknown to", func(d *Definition) {
			d.Edges[0].To = "nope"
		}, "is not a declared node"},
		{"edge has both to and router", func(d *Definition) {
			d.Edges[0].Router = &Router{On: "$a.output.x", Branches: []Branch{{When: true, To: "b"}}}
		}, "both to and router"},
		{"edge has neither to nor router", func(d *Definition) {
			d.Edges[0].To = ""
		}, "neither to nor router"},
		{"router bad expression", func(d *Definition) {
			d.Edges[0].To = ""
			d.Edges[0].Router = &Router{On: "bogus", Branches: []Branch{{When: true, To: "b"}}}
		}, "not a valid expression"},
		{"router refers to unknown node", func(d *Definition) {
			d.Edges[0].To = ""
			d.Edges[0].Router = &Router{On: "$zzz.output.x", Branches: []Branch{{When: true, To: "b"}}}
		}, "router.on references unknown node"},
		{"router branch missing destination", func(d *Definition) {
			d.Edges[0].To = ""
			d.Edges[0].Router = &Router{On: "$a.output.x", Branches: []Branch{{When: true, To: ""}}}
		}, "branch"},
		{"router destination unknown", func(d *Definition) {
			d.Edges[0].To = ""
			d.Edges[0].Router = &Router{On: "$a.output.x", Branches: []Branch{{When: true, To: "zzz"}}}
		}, "not a declared node"},
		{"router default unknown", func(d *Definition) {
			d.Edges[0].To = ""
			d.Edges[0].Router = &Router{On: "$a.output.x", Default: "zzz"}
		}, "not a declared node"},
		{"negative max_iterations", func(d *Definition) { d.MaxIterations = -1 }, "max_iterations"},
		{"exit not a node", func(d *Definition) { d.Exit = "zzz" }, "exit"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := validDef()
			tc.mutate(def)
			err := def.Validate()
			if err == nil {
				t.Fatalf("Validate: got nil, want error containing %q", tc.contains)
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("Validate: %v, want to contain %q", err, tc.contains)
			}
		})
	}
}

func TestValidate_RouterDefaultOnlyOK(t *testing.T) {
	def := &Definition{
		ID:    "g",
		Entry: "a",
		Nodes: []Node{{ID: "a", Role: "investigator"}, {ID: "b", Role: "planner"}},
		Edges: []Edge{
			{From: "a", Router: &Router{On: "$a.output.x", Default: "b"}},
			{From: "b", To: EndNode},
		},
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
