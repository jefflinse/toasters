package progress

import (
	"encoding/json"
	"testing"
)

func TestProgressToolDefs_Count(t *testing.T) {
	t.Parallel()
	defs := ProgressToolDefs()
	if len(defs) != 6 {
		t.Errorf("ProgressToolDefs() returned %d tools, want 6", len(defs))
	}
}

func TestProgressToolDefs_Names(t *testing.T) {
	t.Parallel()
	defs := ProgressToolDefs()

	wantNames := []string{
		"report_progress",
		"report_blocker",
		"update_task_status",
		"request_review",
		"query_job_context",
		"log_artifact",
	}

	nameSet := make(map[string]bool, len(defs))
	for _, d := range defs {
		nameSet[d.Name] = true
	}

	for _, name := range wantNames {
		if !nameSet[name] {
			t.Errorf("tool %q not found in ProgressToolDefs()", name)
		}
	}
}

func TestProgressToolDefs_NoDuplicateNames(t *testing.T) {
	t.Parallel()
	defs := ProgressToolDefs()

	seen := make(map[string]int)
	for _, d := range defs {
		seen[d.Name]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("tool name %q appears %d times, want 1", name, count)
		}
	}
}

func TestProgressToolDefs_ValidJSON(t *testing.T) {
	t.Parallel()
	defs := ProgressToolDefs()

	for _, d := range defs {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			if len(d.Parameters) == 0 {
				t.Errorf("tool %q has empty Parameters", d.Name)
				return
			}
			var schema map[string]any
			if err := json.Unmarshal(d.Parameters, &schema); err != nil {
				t.Errorf("tool %q Parameters is not valid JSON: %v\nraw: %s", d.Name, err, string(d.Parameters))
			}
		})
	}
}

func TestProgressToolDefs_HasDescriptions(t *testing.T) {
	t.Parallel()
	defs := ProgressToolDefs()

	for _, d := range defs {
		if d.Description == "" {
			t.Errorf("tool %q has empty Description", d.Name)
		}
	}
}

func TestProgressToolDefs_RequiredFields(t *testing.T) {
	t.Parallel()
	defs := ProgressToolDefs()

	// Each tool should declare at least one required field.
	for _, d := range defs {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			var schema map[string]any
			if err := json.Unmarshal(d.Parameters, &schema); err != nil {
				t.Fatalf("invalid JSON schema for %q: %v", d.Name, err)
			}

			required, ok := schema["required"]
			if !ok {
				t.Errorf("tool %q schema has no 'required' field", d.Name)
				return
			}

			requiredSlice, ok := required.([]any)
			if !ok {
				t.Errorf("tool %q 'required' is not an array", d.Name)
				return
			}

			if len(requiredSlice) == 0 {
				t.Errorf("tool %q has empty 'required' array", d.Name)
			}
		})
	}
}

func TestProgressToolDefs_SchemaType(t *testing.T) {
	t.Parallel()
	defs := ProgressToolDefs()

	for _, d := range defs {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			var schema map[string]any
			if err := json.Unmarshal(d.Parameters, &schema); err != nil {
				t.Fatalf("invalid JSON schema for %q: %v", d.Name, err)
			}

			typ, ok := schema["type"]
			if !ok {
				t.Errorf("tool %q schema has no 'type' field", d.Name)
				return
			}
			if typ != "object" {
				t.Errorf("tool %q schema type = %q, want %q", d.Name, typ, "object")
			}
		})
	}
}

func TestProgressToolDefs_HasProperties(t *testing.T) {
	t.Parallel()
	defs := ProgressToolDefs()

	for _, d := range defs {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			var schema map[string]any
			if err := json.Unmarshal(d.Parameters, &schema); err != nil {
				t.Fatalf("invalid JSON schema for %q: %v", d.Name, err)
			}

			props, ok := schema["properties"]
			if !ok {
				t.Errorf("tool %q schema has no 'properties' field", d.Name)
				return
			}

			propsMap, ok := props.(map[string]any)
			if !ok {
				t.Errorf("tool %q 'properties' is not an object", d.Name)
				return
			}

			if len(propsMap) == 0 {
				t.Errorf("tool %q has no properties defined", d.Name)
			}
		})
	}
}

func TestProgressToolDefs_SpecificRequiredFields(t *testing.T) {
	t.Parallel()

	// Verify the required fields for each tool match the documented schema.
	tests := []struct {
		toolName string
		required []string
	}{
		{"report_progress", []string{"job_id", "status", "message"}},
		{"report_blocker", []string{"job_id", "description", "severity"}},
		{"update_task_status", []string{"job_id", "task_id", "status"}},
		{"request_review", []string{"job_id", "artifact_path"}},
		{"query_job_context", []string{"job_id"}},
		{"log_artifact", []string{"job_id", "type", "path"}},
	}

	defs := ProgressToolDefs()
	defsByName := make(map[string]ToolDef, len(defs))
	for _, d := range defs {
		defsByName[d.Name] = d
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			t.Parallel()
			d, ok := defsByName[tt.toolName]
			if !ok {
				t.Fatalf("tool %q not found", tt.toolName)
			}

			var schema map[string]any
			if err := json.Unmarshal(d.Parameters, &schema); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}

			rawRequired, _ := schema["required"].([]any)
			requiredSet := make(map[string]bool, len(rawRequired))
			for _, r := range rawRequired {
				if s, ok := r.(string); ok {
					requiredSet[s] = true
				}
			}

			for _, field := range tt.required {
				if !requiredSet[field] {
					t.Errorf("tool %q: expected %q in required fields, got %v", tt.toolName, field, rawRequired)
				}
			}
		})
	}
}

func TestProgressToolDefs_Immutable(t *testing.T) {
	t.Parallel()
	// Calling ProgressToolDefs() twice should return independent slices.
	defs1 := ProgressToolDefs()
	defs2 := ProgressToolDefs()

	if len(defs1) != len(defs2) {
		t.Errorf("successive calls returned different lengths: %d vs %d", len(defs1), len(defs2))
	}
}
