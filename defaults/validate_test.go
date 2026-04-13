package defaults_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/jefflinse/toasters/defaults"
	"github.com/jefflinse/toasters/internal/mdfmt"
)

func TestSystemFilesParseCorrectly(t *testing.T) {
	// Extract embedded files to a temp dir.
	tmpDir := t.TempDir()

	err := fs.WalkDir(defaults.SystemFiles, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmpDir, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(defaults.SystemFiles, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("extracting embedded files: %v", err)
	}

	tests := []struct {
		path    string
		wantDef mdfmt.DefType
		name    string
	}{
		{"system/team.md", mdfmt.DefTeam, "System"},
		{"system/skills/orchestration.md", mdfmt.DefSkill, "Orchestration"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			fullPath := filepath.Join(tmpDir, tt.path)
			defType, def, err := mdfmt.ParseFile(fullPath)
			if err != nil {
				t.Fatalf("ParseFile(%s): %v", tt.path, err)
			}
			if defType != tt.wantDef {
				t.Errorf("DefType = %q, want %q", defType, tt.wantDef)
			}

			// Verify name matches.
			switch d := def.(type) {
			case *mdfmt.TeamDef:
				if d.Name != tt.name {
					t.Errorf("Name = %q, want %q", d.Name, tt.name)
				}
				if d.Lead == "" {
					t.Error("Lead is empty")
				}
				if d.Body == "" {
					t.Error("Body is empty")
				}
			case *mdfmt.SkillDef:
				if d.Name != tt.name {
					t.Errorf("Name = %q, want %q", d.Name, tt.name)
				}
				if d.Body == "" {
					t.Error("Body is empty")
				}
			}
		})
	}
}
