package frontmatter_test

import (
	"testing"

	"github.com/jefflinse/toasters/internal/frontmatter"
)

func TestSplit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantLines []string
		wantBody  string
		wantErr   string // substring of error message; empty means no error expected
	}{
		{
			name:      "valid frontmatter",
			input:     "---\nid: foo\nname: bar\n---\nbody text",
			wantLines: []string{"id: foo", "name: bar"},
			wantBody:  "body text",
		},
		{
			name:      "empty frontmatter block",
			input:     "---\n---\nbody",
			wantLines: []string{},
			wantBody:  "body",
		},
		{
			name:    "no opening delimiter",
			input:   "just some text",
			wantErr: "no frontmatter delimiter found",
		},
		{
			name:    "opening but no closing delimiter",
			input:   "---\nid: foo\nno closing",
			wantErr: "frontmatter closing delimiter not found",
		},
		{
			name:      "body with multiple lines",
			input:     "---\nk: v\n---\nline1\nline2\nline3",
			wantLines: []string{"k: v"},
			wantBody:  "line1\nline2\nline3",
		},
		{
			name:      "body preserves leading newline",
			input:     "---\nk: v\n---\n\nbody",
			wantLines: []string{"k: v"},
			wantBody:  "\nbody",
		},
		{
			name:      "empty body",
			input:     "---\nk: v\n---",
			wantLines: []string{"k: v"},
			wantBody:  "",
		},
		{
			name:      "frontmatter with empty lines between keys",
			input:     "---\nk1: v1\n\nk2: v2\n---\nbody",
			wantLines: []string{"k1: v1", "", "k2: v2"},
			wantBody:  "body",
		},
		{
			name:      "multiple frontmatter lines",
			input:     "---\na: 1\nb: 2\nc: 3\n---\n",
			wantLines: []string{"a: 1", "b: 2", "c: 3"},
			wantBody:  "",
		},
		{
			name:    "delimiter with leading space is not recognized as opening",
			input:   " ---\nk: v\n---\nbody",
			wantErr: "frontmatter closing delimiter not found",
		},
		{
			name:    "delimiter with trailing space is not recognized as opening",
			input:   "--- \nk: v\n---\nbody",
			wantErr: "frontmatter closing delimiter not found",
		},
		{
			name:      "body contains triple dashes",
			input:     "---\nk: v\n---\nbefore\n---\nafter",
			wantLines: []string{"k: v"},
			wantBody:  "before\n---\nafter",
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: "no frontmatter delimiter found",
		},
		{
			name:    "only opening delimiter",
			input:   "---",
			wantErr: "frontmatter closing delimiter not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotLines, gotBody, err := frontmatter.Split(tt.input)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if got := err.Error(); !contains(got, tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", got, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !sliceEqual(gotLines, tt.wantLines) {
				t.Errorf("lines = %q, want %q", gotLines, tt.wantLines)
			}

			if gotBody != tt.wantBody {
				t.Errorf("body = %q, want %q", gotBody, tt.wantBody)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantKV   map[string]string
		wantBody string
		wantErr  string
	}{
		{
			name:     "standard key-value pairs",
			input:    "---\nid: foo\nname: bar\n---\nbody",
			wantKV:   map[string]string{"id": "foo", "name": "bar"},
			wantBody: "body",
		},
		{
			name:     "key with empty value via colon-space",
			input:    "---\nkey: \n---\n",
			wantKV:   map[string]string{"key": ""},
			wantBody: "",
		},
		{
			name:     "key with no value just colon",
			input:    "---\nkey:\n---\n",
			wantKV:   map[string]string{"key": ""},
			wantBody: "",
		},
		{
			name:     "value containing colons",
			input:    "---\nurl: http://example.com:8080\n---\n",
			wantKV:   map[string]string{"url": "http://example.com:8080"},
			wantBody: "",
		},
		{
			name:     "empty frontmatter block",
			input:    "---\n---\nbody",
			wantKV:   map[string]string{},
			wantBody: "body",
		},
		{
			name:    "no delimiter returns error",
			input:   "no frontmatter",
			wantErr: "no frontmatter delimiter found",
		},
		{
			name:  "mixed keys",
			input: "---\nid: 123\nname: test\nstatus: active\ncreated: 2026-01-01T00:00:00Z\n---\n",
			wantKV: map[string]string{
				"id":      "123",
				"name":    "test",
				"status":  "active",
				"created": "2026-01-01T00:00:00Z",
			},
			wantBody: "",
		},
		{
			name:     "empty lines in frontmatter are skipped",
			input:    "---\nk1: v1\n\nk2: v2\n---\nbody",
			wantKV:   map[string]string{"k1": "v1", "k2": "v2"},
			wantBody: "body",
		},
		{
			name:     "key without colon at all",
			input:    "---\norphan\n---\n",
			wantKV:   map[string]string{"orphan": ""},
			wantBody: "",
		},
		{
			name:     "value with colon-space in middle",
			input:    "---\ndesc: hello: world\n---\n",
			wantKV:   map[string]string{"desc": "hello: world"},
			wantBody: "",
		},
		{
			name:     "key with leading spaces is trimmed",
			input:    "---\n  key: value\n---\n",
			wantKV:   map[string]string{"key": "value"},
			wantBody: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotKV, gotBody, err := frontmatter.Parse(tt.input)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if got := err.Error(); !contains(got, tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", got, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(gotKV) != len(tt.wantKV) {
				t.Fatalf("kv map length = %d, want %d\n  got:  %v\n  want: %v", len(gotKV), len(tt.wantKV), gotKV, tt.wantKV)
			}
			for k, wantV := range tt.wantKV {
				gotV, ok := gotKV[k]
				if !ok {
					t.Errorf("missing key %q in result map; got %v", k, gotKV)
					continue
				}
				if gotV != wantV {
					t.Errorf("kv[%q] = %q, want %q", k, gotV, wantV)
				}
			}

			if gotBody != tt.wantBody {
				t.Errorf("body = %q, want %q", gotBody, tt.wantBody)
			}
		})
	}
}

// contains reports whether s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// sliceEqual reports whether two string slices are equal.
// Two nil slices are equal; a nil slice and an empty slice are not.
// However, for our tests we treat a nil slice and a zero-length slice
// from Split (which returns lines[start+1:end]) as equivalent.
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
