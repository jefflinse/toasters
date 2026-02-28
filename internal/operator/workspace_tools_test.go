package operator

import (
	"encoding/json"
	"testing"
)

func TestValidateGitCloneArgs(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		dirName string
		wantErr string // empty means no error expected
	}{
		// --- Valid inputs ---
		{
			name:    "valid https URL",
			url:     "https://github.com/user/repo.git",
			dirName: "repo",
		},
		{
			name:    "valid http URL",
			url:     "http://github.com/user/repo.git",
			dirName: "repo",
		},
		{
			name:    "valid ssh URL",
			url:     "ssh://git@github.com/user/repo.git",
			dirName: "repo",
		},
		{
			name:    "valid git URL",
			url:     "git://github.com/user/repo.git",
			dirName: "my-repo",
		},
		{
			name:    "name with dots underscores hyphens",
			url:     "https://github.com/user/repo.git",
			dirName: "my_repo.v2-beta",
		},

		// --- Attack vector 1: Flag injection via URL ---
		{
			name:    "flag injection via URL --upload-pack",
			url:     "--upload-pack=malicious_command",
			dirName: "repo",
			wantErr: "must not start with '-'",
		},
		{
			name:    "flag injection via URL --config",
			url:     "--config=core.sshCommand=evil",
			dirName: "repo",
			wantErr: "must not start with '-'",
		},
		{
			name:    "flag injection via URL single dash",
			url:     "-o/tmp/evil",
			dirName: "repo",
			wantErr: "must not start with '-'",
		},

		// --- Attack vector 2: ext:: protocol ---
		{
			name:    "ext protocol command execution",
			url:     "ext::sh -c 'echo pwned'",
			dirName: "repo",
			wantErr: "invalid git URL scheme",
		},
		{
			name:    "file protocol not allowed",
			url:     "file:///etc/passwd",
			dirName: "repo",
			wantErr: "invalid git URL scheme",
		},
		{
			name:    "ftp protocol not allowed",
			url:     "ftp://example.com/repo.git",
			dirName: "repo",
			wantErr: "invalid git URL scheme",
		},
		{
			name:    "empty scheme not allowed",
			url:     "just-a-path",
			dirName: "repo",
			wantErr: "invalid git URL scheme",
		},

		// --- Attack vector 3: Name injection ---
		{
			name:    "flag injection via name --config",
			url:     "https://github.com/user/repo.git",
			dirName: "--config=core.sshCommand=malicious",
			wantErr: "must not start with '-'",
		},
		{
			name:    "name with path traversal",
			url:     "https://github.com/user/repo.git",
			dirName: "../../../etc",
			wantErr: "must contain only alphanumeric",
		},
		{
			name:    "name with spaces",
			url:     "https://github.com/user/repo.git",
			dirName: "repo name",
			wantErr: "must contain only alphanumeric",
		},
		{
			name:    "name with shell metacharacters",
			url:     "https://github.com/user/repo.git",
			dirName: "repo;rm -rf /",
			wantErr: "must contain only alphanumeric",
		},
		{
			name:    "name with backticks",
			url:     "https://github.com/user/repo.git",
			dirName: "repo`whoami`",
			wantErr: "must contain only alphanumeric",
		},
		{
			name:    "name with dollar sign",
			url:     "https://github.com/user/repo.git",
			dirName: "repo$(evil)",
			wantErr: "must contain only alphanumeric",
		},
		{
			name:    "empty name",
			url:     "https://github.com/user/repo.git",
			dirName: "",
			wantErr: "must contain only alphanumeric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGitCloneArgs(tt.url, tt.dirName)
			if tt.wantErr == "" {
				assertNoError(t, err)
			} else {
				assertError(t, err)
				assertContains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestRepoName(t *testing.T) {
	tests := []struct {
		url  string
		name string
		want string
	}{
		{"https://github.com/user/repo.git", "", "repo"},
		{"https://github.com/user/repo", "", "repo"},
		{"https://github.com/user/my-project.git", "", "my-project"},
		{"https://github.com/user/repo.git", "custom-name", "custom-name"},
		{"", "", "repo"},       // empty URL → fallback
		{".", "", "repo"},      // dot URL → fallback
		{"/", "", "/"},         // root → path.Base returns "/"
		{"foo/bar", "", "bar"}, // relative path
	}

	for _, tt := range tests {
		t.Run(tt.url+"_"+tt.name, func(t *testing.T) {
			got := repoName(tt.url, tt.name)
			assertEqual(t, tt.want, got)
		})
	}
}

func TestSetupWorkspace_ValidationRejectsAttackVectors(t *testing.T) {
	// This integration test verifies that setupWorkspace properly rejects
	// malicious inputs via the validation layer, recording them as failures
	// in the JSON result rather than executing them.
	st, store, _, _, _ := newTestSystemTools(t)
	ctx := t.Context()

	// Create a job so setupWorkspace can look it up.
	args, _ := json.Marshal(map[string]string{
		"title":       "Security test job",
		"description": "Tests command injection prevention",
	})
	jobResult, err := st.Execute(ctx, "create_job", args)
	assertNoError(t, err)

	var jobRes map[string]string
	if err := json.Unmarshal([]byte(jobResult), &jobRes); err != nil {
		t.Fatalf("parsing job result: %v", err)
	}
	jobID := jobRes["job_id"]

	// Build operatorTools with the real store.
	ot := &operatorTools{store: store}

	t.Run("ext_protocol_rejected", func(t *testing.T) {
		wsArgs, _ := json.Marshal(map[string]any{
			"job_id": jobID,
			"repos": []map[string]string{
				{"url": "ext::sh -c 'echo pwned'"},
			},
		})
		result, err := ot.setupWorkspace(ctx, wsArgs)
		assertNoError(t, err) // setupWorkspace returns success with failures in the result

		assertContains(t, result, "invalid git URL scheme")
		assertContains(t, result, "failed")
	})

	t.Run("flag_injection_url_rejected", func(t *testing.T) {
		wsArgs, _ := json.Marshal(map[string]any{
			"job_id": jobID,
			"repos": []map[string]string{
				{"url": "--upload-pack=malicious"},
			},
		})
		result, err := ot.setupWorkspace(ctx, wsArgs)
		assertNoError(t, err)

		assertContains(t, result, "must not start with '-'")
		assertContains(t, result, "failed")
	})

	t.Run("flag_injection_name_rejected", func(t *testing.T) {
		wsArgs, _ := json.Marshal(map[string]any{
			"job_id": jobID,
			"repos": []map[string]string{
				{"url": "https://github.com/user/repo.git", "name": "--config=core.sshCommand=evil"},
			},
		})
		result, err := ot.setupWorkspace(ctx, wsArgs)
		assertNoError(t, err)

		assertContains(t, result, "must not start with '-'")
		assertContains(t, result, "failed")
	})

	t.Run("path_traversal_name_rejected", func(t *testing.T) {
		wsArgs, _ := json.Marshal(map[string]any{
			"job_id": jobID,
			"repos": []map[string]string{
				{"url": "https://github.com/user/repo.git", "name": "../../../etc/passwd"},
			},
		})
		result, err := ot.setupWorkspace(ctx, wsArgs)
		assertNoError(t, err)

		assertContains(t, result, "must contain only alphanumeric")
		assertContains(t, result, "failed")
	})
}
