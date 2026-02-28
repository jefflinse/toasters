package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/db"
)

// validRepoName matches alphanumeric characters, dots, underscores, and hyphens.
var validRepoName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// allowedGitSchemes is the set of URL schemes permitted for git clone.
var allowedGitSchemes = map[string]bool{
	"https": true,
	"http":  true,
	"ssh":   true,
	"git":   true,
}

const cloneTimeout = 5 * time.Minute

// setupWorkspace clones one or more git repositories into the job's workspace
// directory and sets the job status to setting_up while running.
func (ot *operatorTools) setupWorkspace(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		JobID string `json:"job_id"`
		Repos []struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing setup_workspace args: %w", err)
	}

	if params.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	if len(params.Repos) == 0 {
		return "", fmt.Errorf("repos is required and must not be empty")
	}

	// 1. Look up the job to get its workspace directory.
	job, err := ot.store.GetJob(ctx, params.JobID)
	if err != nil {
		return "", fmt.Errorf("getting job %q: %w", params.JobID, err)
	}

	// 2. Transition job to setting_up.
	if err := ot.store.UpdateJobStatus(ctx, params.JobID, db.JobStatusSettingUp); err != nil {
		return "", fmt.Errorf("setting job status to setting_up: %w", err)
	}

	slog.Info("setting up workspace",
		"job_id", params.JobID,
		"workspace", job.WorkspaceDir,
		"repo_count", len(params.Repos),
	)

	type failedEntry struct {
		Name  string `json:"name"`
		Error string `json:"error"`
	}

	var cloned []string
	var failed []failedEntry

	// 3. Clone each repo.
	for _, repo := range params.Repos {
		name := repoName(repo.URL, repo.Name)

		// Validate inputs to prevent command injection via git clone.
		if err := validateGitCloneArgs(repo.URL, name); err != nil {
			failed = append(failed, failedEntry{Name: name, Error: err.Error()})
			continue
		}

		cloneCtx, cancel := context.WithTimeout(ctx, cloneTimeout)
		var out bytes.Buffer
		// Use "--" to separate flags from positional arguments, preventing
		// flag injection if a URL or name starts with "-".
		cmd := exec.CommandContext(cloneCtx, "git", "clone", "--", repo.URL, name)
		cmd.Dir = job.WorkspaceDir
		cmd.Stdout = &out
		cmd.Stderr = &out
		runErr := cmd.Run()
		cancel()

		if runErr != nil {
			errMsg := strings.TrimSpace(out.String())
			if errMsg == "" {
				errMsg = runErr.Error()
			}
			slog.Warn("git clone failed",
				"job_id", params.JobID,
				"repo", repo.URL,
				"name", name,
				"error", errMsg,
			)
			failed = append(failed, failedEntry{Name: name, Error: errMsg})
		} else {
			slog.Info("cloned repo",
				"job_id", params.JobID,
				"repo", repo.URL,
				"name", name,
			)
			cloned = append(cloned, name)
		}
	}

	// 4. Build and return the JSON summary.
	summary := map[string]any{
		"workspace": job.WorkspaceDir,
		"cloned":    cloned,
		"failed":    failed,
	}
	// Use empty slices instead of null in JSON output.
	if cloned == nil {
		summary["cloned"] = []string{}
	}
	if failed == nil {
		summary["failed"] = []failedEntry{}
	}

	result, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("marshaling setup_workspace result: %w", err)
	}
	return string(result), nil
}

// repoName derives a directory name from a git URL, using the explicit name
// if provided. It strips a trailing ".git" suffix and takes the last path
// segment of the URL.
func repoName(rawURL, name string) string {
	if name != "" {
		return name
	}
	// Use path (not filepath) so we handle URL separators correctly on all OSes.
	base := path.Base(rawURL)
	base = strings.TrimSuffix(base, ".git")
	if base == "" || base == "." {
		return "repo"
	}
	return base
}

// validateGitCloneArgs validates the URL and directory name passed to git clone
// to prevent command injection attacks. It checks:
//  1. Neither argument starts with "-" (flag injection)
//  2. URL has an allowed scheme (https, http, ssh, git)
//  3. Directory name contains only safe characters
func validateGitCloneArgs(rawURL, name string) error {
	// Reject flag injection: arguments starting with "-" could be interpreted
	// as git flags (e.g. --upload-pack, --config).
	if strings.HasPrefix(rawURL, "-") {
		return fmt.Errorf("invalid git URL: must not start with '-'")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid repo name: must not start with '-'")
	}

	// Validate URL scheme to block dangerous protocols like ext:: which
	// allows arbitrary command execution.
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid git URL: %w", err)
	}
	if !allowedGitSchemes[u.Scheme] {
		return fmt.Errorf("invalid git URL scheme %q: only https, http, ssh, and git are allowed", u.Scheme)
	}

	// Validate repo name: only alphanumeric, dots, underscores, and hyphens.
	if !validRepoName.MatchString(name) {
		return fmt.Errorf("invalid repo name %q: must contain only alphanumeric characters, dots, underscores, and hyphens", name)
	}

	return nil
}
