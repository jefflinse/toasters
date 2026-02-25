package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSlimJSON_NotJSON(t *testing.T) {
	input := "this is not json"
	got := SlimJSON(input)
	if got != input {
		t.Errorf("expected unchanged non-JSON input, got %q", got)
	}
}

func TestSlimJSON_InvalidJSON(t *testing.T) {
	input := `{"broken": `
	got := SlimJSON(input)
	if got != input {
		t.Errorf("expected unchanged invalid JSON, got %q", got)
	}
}

func TestSlimJSON_NullValues(t *testing.T) {
	input := `{"name":"alice","bio":null,"age":30,"address":null}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["bio"]; ok {
		t.Error("expected 'bio' (null) to be removed")
	}
	if _, ok := parsed["address"]; ok {
		t.Error("expected 'address' (null) to be removed")
	}
	if parsed["name"] != "alice" {
		t.Errorf("expected 'name' to be preserved, got %v", parsed["name"])
	}
	if parsed["age"] != float64(30) {
		t.Errorf("expected 'age' to be preserved, got %v", parsed["age"])
	}
}

func TestSlimJSON_URLFields(t *testing.T) {
	input := `{
		"id": 1,
		"avatar_url": "https://avatars.githubusercontent.com/u/123",
		"repos_url": "https://api.github.com/users/foo/repos",
		"followers_url": "https://api.github.com/users/foo/followers",
		"login": "foo"
	}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["avatar_url"]; ok {
		t.Error("expected 'avatar_url' to be removed")
	}
	if _, ok := parsed["repos_url"]; ok {
		t.Error("expected 'repos_url' to be removed")
	}
	if _, ok := parsed["followers_url"]; ok {
		t.Error("expected 'followers_url' to be removed")
	}
	if parsed["login"] != "foo" {
		t.Error("expected 'login' to be preserved")
	}
	if parsed["id"] != float64(1) {
		t.Error("expected 'id' to be preserved")
	}
}

func TestSlimJSON_HTMLURLPreservedOnPrimaryResource(t *testing.T) {
	input := `{
		"title": "Fix the bug",
		"html_url": "https://github.com/org/repo/issues/1",
		"avatar_url": "https://avatars.githubusercontent.com/u/123",
		"state": "open"
	}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["html_url"]; !ok {
		t.Error("expected 'html_url' to be preserved on primary resource (has 'title')")
	}
	if _, ok := parsed["avatar_url"]; ok {
		t.Error("expected 'avatar_url' to be removed")
	}
	if parsed["title"] != "Fix the bug" {
		t.Error("expected 'title' to be preserved")
	}
}

func TestSlimJSON_HTMLURLRemovedOnNonPrimary(t *testing.T) {
	// Object without content-bearing fields — html_url should be removed.
	input := `{
		"id": 42,
		"html_url": "https://github.com/org/repo",
		"login": "foo"
	}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["html_url"]; ok {
		t.Error("expected 'html_url' to be removed on non-primary resource")
	}
}

func TestSlimJSON_HTMLURLPreservedWithName(t *testing.T) {
	input := `{"name":"my-repo","html_url":"https://github.com/org/my-repo"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["html_url"]; !ok {
		t.Error("expected 'html_url' to be preserved on object with 'name'")
	}
}

func TestSlimJSON_HTMLURLPreservedWithBody(t *testing.T) {
	input := `{"body":"Some content","html_url":"https://github.com/org/repo/pulls/1"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["html_url"]; !ok {
		t.Error("expected 'html_url' to be preserved on object with 'body'")
	}
}

func TestSlimJSON_HTMLURLPreservedWithDescription(t *testing.T) {
	input := `{"description":"A cool project","html_url":"https://github.com/org/repo"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["html_url"]; !ok {
		t.Error("expected 'html_url' to be preserved on object with 'description'")
	}
}

func TestSlimJSON_HTMLURLPreservedWithFullName(t *testing.T) {
	input := `{"full_name":"org/repo","html_url":"https://github.com/org/repo"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["html_url"]; !ok {
		t.Error("expected 'html_url' to be preserved on object with 'full_name'")
	}
}

func TestSlimJSON_URLFieldDropped(t *testing.T) {
	input := `{"url":"https://api.github.com/repos/org/repo","id":1}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["url"]; ok {
		t.Error("expected 'url' with HTTP value to be removed")
	}
}

func TestSlimJSON_URLFieldPreservedNonHTTP(t *testing.T) {
	input := `{"url":"file:///local/path","id":1}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["url"]; !ok {
		t.Error("expected 'url' with non-HTTP value to be preserved")
	}
}

func TestSlimJSON_URLTemplates(t *testing.T) {
	input := `{
		"id": 1,
		"following_url": "https://api.github.com/users/foo/following{/other_user}",
		"starred_url": "https://api.github.com/users/foo/starred{/owner}{/repo}",
		"login": "foo"
	}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// These are also _url fields, so they'd be caught by Rule 2 as well.
	// But the template rule (Rule 3) also applies.
	if _, ok := parsed["following_url"]; ok {
		t.Error("expected 'following_url' to be removed")
	}
	if _, ok := parsed["starred_url"]; ok {
		t.Error("expected 'starred_url' to be removed")
	}
}

func TestSlimJSON_TemplateStringNonURL(t *testing.T) {
	// A non-URL field with a URL template value should still be dropped.
	input := `{"id":1,"some_template":"https://example.com/{id}/details"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["some_template"]; ok {
		t.Error("expected URL template string to be removed")
	}
}

func TestSlimJSON_NonURLBracesPreserved(t *testing.T) {
	// Non-URL strings containing braces should be preserved (e.g., code, user text).
	input := `{"body":"Use fmt.Sprintf(\"%s\", {name})","title":"Fix the {thing}"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["body"]; !ok {
		t.Error("expected 'body' with braces to be preserved (not a URL template)")
	}
	if _, ok := parsed["title"]; !ok {
		t.Error("expected 'title' with braces to be preserved (not a URL template)")
	}
}

func TestSlimJSON_APIDomainURLs(t *testing.T) {
	input := `{
		"id": 1,
		"commits": "https://api.github.com/repos/org/repo/commits",
		"events": "https://api.example.com/events/123",
		"homepage": "https://example.com"
	}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["commits"]; ok {
		t.Error("expected 'commits' (api.github.com URL) to be removed")
	}
	if _, ok := parsed["events"]; ok {
		t.Error("expected 'events' (api.example.com URL) to be removed")
	}
	if parsed["homepage"] != "https://example.com" {
		t.Error("expected 'homepage' (non-api URL) to be preserved")
	}
}

func TestSlimJSON_NodeIDAndGravatarID(t *testing.T) {
	input := `{"id":1,"node_id":"MDQ6VXNlcjE=","gravatar_id":"abc123","login":"foo"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["node_id"]; ok {
		t.Error("expected 'node_id' to be removed")
	}
	if _, ok := parsed["gravatar_id"]; ok {
		t.Error("expected 'gravatar_id' to be removed")
	}
	if parsed["login"] != "foo" {
		t.Error("expected 'login' to be preserved")
	}
}

func TestSlimJSON_LargeOpaqueBlob_PGP(t *testing.T) {
	pgpKey := "-----BEGIN " + strings.Repeat("A", 600)
	input := `{"id":1,"verification_key":"` + pgpKey + `"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["verification_key"]; ok {
		t.Error("expected PGP key blob to be removed")
	}
}

func TestSlimJSON_LargeOpaqueBlob_Base64(t *testing.T) {
	base64Blob := strings.Repeat("ABCDEFGHabcdefgh12345678+/==", 30) // >500 chars
	input := `{"id":1,"signature":"` + base64Blob + `"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["signature"]; ok {
		t.Error("expected base64 blob to be removed")
	}
}

func TestSlimJSON_LargeString_NotOpaque(t *testing.T) {
	// A large string with non-base64 characters should be preserved.
	longText := strings.Repeat("Hello, world! This is a normal text. ", 20) // >500 chars
	input := `{"id":1,"body":"` + longText + `"}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["body"]; !ok {
		t.Error("expected large non-opaque string to be preserved")
	}
}

func TestSlimJSON_ShortString_NotDropped(t *testing.T) {
	// A short base64-like string should NOT be dropped (under 500 chars).
	input := `{"id":1,"token":"abc123=="}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["token"]; !ok {
		t.Error("expected short string to be preserved even if base64-like")
	}
}

func TestSlimJSON_RecursesIntoNestedObjects(t *testing.T) {
	input := `{
		"title": "PR Title",
		"user": {
			"login": "alice",
			"id": 42,
			"avatar_url": "https://avatars.githubusercontent.com/u/42",
			"repos_url": "https://api.github.com/users/alice/repos",
			"node_id": "MDQ6VXNlcjQy",
			"type": "User",
			"site_admin": false,
			"bio": null
		}
	}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	user, ok := parsed["user"].(map[string]any)
	if !ok {
		t.Fatal("expected 'user' to be an object")
	}

	if user["login"] != "alice" {
		t.Error("expected nested 'login' to be preserved")
	}
	if user["id"] != float64(42) {
		t.Error("expected nested 'id' to be preserved")
	}
	if user["type"] != "User" {
		t.Error("expected nested 'type' to be preserved")
	}
	if _, ok := user["avatar_url"]; ok {
		t.Error("expected nested 'avatar_url' to be removed")
	}
	if _, ok := user["repos_url"]; ok {
		t.Error("expected nested 'repos_url' to be removed")
	}
	if _, ok := user["node_id"]; ok {
		t.Error("expected nested 'node_id' to be removed")
	}
	if _, ok := user["bio"]; ok {
		t.Error("expected nested null 'bio' to be removed")
	}
}

func TestSlimJSON_RecursesIntoArrays(t *testing.T) {
	input := `[
		{"id":1,"node_id":"abc","avatar_url":"https://example.com/1.png","login":"alice"},
		{"id":2,"node_id":"def","avatar_url":"https://example.com/2.png","login":"bob"}
	]`
	got := SlimJSON(input)

	var parsed []map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON array: %v", err)
	}

	if len(parsed) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(parsed))
	}

	for i, item := range parsed {
		if _, ok := item["node_id"]; ok {
			t.Errorf("element %d: expected 'node_id' to be removed", i)
		}
		if _, ok := item["avatar_url"]; ok {
			t.Errorf("element %d: expected 'avatar_url' to be removed", i)
		}
		if _, ok := item["login"]; !ok {
			t.Errorf("element %d: expected 'login' to be preserved", i)
		}
	}
}

func TestSlimJSON_Idempotent(t *testing.T) {
	input := `{
		"title": "Test",
		"html_url": "https://github.com/org/repo/issues/1",
		"avatar_url": "https://avatars.githubusercontent.com/u/1",
		"node_id": "MDQ6VXNlcjE=",
		"bio": null,
		"user": {
			"login": "alice",
			"avatar_url": "https://avatars.githubusercontent.com/u/42"
		}
	}`

	first := SlimJSON(input)
	second := SlimJSON(first)

	if first != second {
		t.Errorf("SlimJSON is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
}

func TestSlimJSON_PreservesJSONValidity(t *testing.T) {
	input := `{
		"id": 1,
		"title": "Test Issue",
		"html_url": "https://github.com/org/repo/issues/1",
		"avatar_url": "https://avatars.githubusercontent.com/u/1",
		"node_id": "MDQ6VXNlcjE=",
		"labels": [
			{"id": 10, "name": "bug", "node_id": "LA_abc", "url": "https://api.github.com/labels/10"},
			{"id": 20, "name": "urgent", "node_id": "LA_def", "url": "https://api.github.com/labels/20"}
		],
		"user": {
			"login": "alice",
			"id": 42,
			"avatar_url": "https://avatars.githubusercontent.com/u/42",
			"node_id": "MDQ6VXNlcjQy"
		},
		"body": "This is the issue body",
		"state": "open",
		"assignee": null
	}`

	got := SlimJSON(input)

	var parsed any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("slimmed result is not valid JSON: %v\nresult: %s", err, got)
	}
}

func TestSlimJSON_EmptyObject(t *testing.T) {
	input := `{}`
	got := SlimJSON(input)
	if got != `{}` {
		t.Errorf("expected empty object unchanged, got %q", got)
	}
}

func TestSlimJSON_EmptyArray(t *testing.T) {
	input := `[]`
	got := SlimJSON(input)
	if got != `[]` {
		t.Errorf("expected empty array unchanged, got %q", got)
	}
}

func TestSlimJSON_ScalarJSON(t *testing.T) {
	// JSON scalars should pass through unchanged.
	cases := []string{`42`, `"hello"`, `true`, `false`}
	for _, input := range cases {
		got := SlimJSON(input)
		if got != input {
			t.Errorf("expected scalar %q unchanged, got %q", input, got)
		}
	}
}

func TestSlimJSON_URLFieldNonStringValue(t *testing.T) {
	// A _url field with a non-string value should be preserved.
	input := `{"avatar_url":42,"id":1}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := parsed["avatar_url"]; !ok {
		t.Error("expected 'avatar_url' with non-string value to be preserved")
	}
}

func TestSlimJSON_RealisticGitHubIssue(t *testing.T) {
	// Simulate a realistic GitHub issue response.
	input := `{
		"id": 123456789,
		"node_id": "I_kwDOABcdef12345",
		"url": "https://api.github.com/repos/org/repo/issues/42",
		"repository_url": "https://api.github.com/repos/org/repo",
		"labels_url": "https://api.github.com/repos/org/repo/issues/42/labels{/name}",
		"comments_url": "https://api.github.com/repos/org/repo/issues/42/comments",
		"events_url": "https://api.github.com/repos/org/repo/issues/42/events",
		"html_url": "https://github.com/org/repo/issues/42",
		"number": 42,
		"state": "open",
		"title": "Something is broken",
		"body": "When I click the button, nothing happens.",
		"user": {
			"login": "reporter",
			"id": 111,
			"node_id": "MDQ6VXNlcjExMQ==",
			"avatar_url": "https://avatars.githubusercontent.com/u/111?v=4",
			"gravatar_id": "",
			"url": "https://api.github.com/users/reporter",
			"html_url": "https://github.com/reporter",
			"followers_url": "https://api.github.com/users/reporter/followers",
			"following_url": "https://api.github.com/users/reporter/following{/other_user}",
			"gists_url": "https://api.github.com/users/reporter/gists{/gist_id}",
			"starred_url": "https://api.github.com/users/reporter/starred{/owner}{/repo}",
			"subscriptions_url": "https://api.github.com/users/reporter/subscriptions",
			"organizations_url": "https://api.github.com/users/reporter/organizations",
			"repos_url": "https://api.github.com/users/reporter/repos",
			"events_url": "https://api.github.com/users/reporter/events{/privacy}",
			"received_events_url": "https://api.github.com/users/reporter/received_events",
			"type": "User",
			"site_admin": false
		},
		"labels": [],
		"assignee": null,
		"assignees": [],
		"milestone": null,
		"comments": 3,
		"created_at": "2024-01-15T10:30:00Z",
		"updated_at": "2024-01-16T14:20:00Z",
		"closed_at": null
	}`

	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// Primary resource fields preserved.
	if parsed["title"] != "Something is broken" {
		t.Error("expected 'title' preserved")
	}
	if parsed["body"] != "When I click the button, nothing happens." {
		t.Error("expected 'body' preserved")
	}
	if parsed["state"] != "open" {
		t.Error("expected 'state' preserved")
	}
	if parsed["number"] != float64(42) {
		t.Error("expected 'number' preserved")
	}
	if _, ok := parsed["html_url"]; !ok {
		t.Error("expected 'html_url' preserved on primary resource")
	}

	// Bloat removed.
	if _, ok := parsed["node_id"]; ok {
		t.Error("expected 'node_id' removed")
	}
	if _, ok := parsed["url"]; ok {
		t.Error("expected 'url' removed")
	}
	if _, ok := parsed["repository_url"]; ok {
		t.Error("expected 'repository_url' removed")
	}
	if _, ok := parsed["labels_url"]; ok {
		t.Error("expected 'labels_url' removed")
	}
	if _, ok := parsed["comments_url"]; ok {
		t.Error("expected 'comments_url' removed")
	}
	if _, ok := parsed["events_url"]; ok {
		t.Error("expected 'events_url' removed")
	}
	if _, ok := parsed["assignee"]; ok {
		t.Error("expected null 'assignee' removed")
	}
	if _, ok := parsed["milestone"]; ok {
		t.Error("expected null 'milestone' removed")
	}
	if _, ok := parsed["closed_at"]; ok {
		t.Error("expected null 'closed_at' removed")
	}

	// User object should be slimmed.
	user, ok := parsed["user"].(map[string]any)
	if !ok {
		t.Fatal("expected 'user' to be an object")
	}
	if user["login"] != "reporter" {
		t.Error("expected user 'login' preserved")
	}
	if _, ok := user["avatar_url"]; ok {
		t.Error("expected user 'avatar_url' removed")
	}
	if _, ok := user["node_id"]; ok {
		t.Error("expected user 'node_id' removed")
	}
	if _, ok := user["gravatar_id"]; ok {
		t.Error("expected user 'gravatar_id' removed")
	}

	// Verify significant size reduction.
	reduction := float64(len(got)) / float64(len(input))
	if reduction > 0.5 {
		t.Errorf("expected >50%% size reduction, got %.1f%% of original (original=%d, slimmed=%d)",
			reduction*100, len(input), len(got))
	}
}

func TestSlimJSON_TruncateResultIntegration(t *testing.T) {
	// Verify that TruncateResult calls SlimJSON before truncation.
	// Build a JSON object with lots of bloat that slimming will remove.
	obj := map[string]any{
		"title":          "Test",
		"body":           "Content",
		"node_id":        "MDQ6VXNlcjE=",
		"avatar_url":     "https://avatars.githubusercontent.com/u/1",
		"url":            "https://api.github.com/repos/org/repo",
		"repository_url": "https://api.github.com/repos/org/repo",
		"labels_url":     "https://api.github.com/repos/org/repo/labels{/name}",
		"assignee":       nil,
		"milestone":      nil,
	}
	data, _ := json.Marshal(obj)
	input := string(data)

	// Set maxLen larger than the slimmed result but smaller than the original.
	// The slimmed result should be much smaller than the original.
	slimmed := SlimJSON(input)
	maxLen := len(slimmed) + 100 // plenty of room for slimmed, but less than original

	if len(input) <= maxLen {
		t.Skip("original input is already under maxLen")
	}

	got := TruncateResult(input, maxLen)

	// Should NOT contain truncation markers — slimming should have brought it under the limit.
	if strings.Contains(got, "truncated") {
		t.Error("expected slimming to bring result under limit without truncation")
	}

	// Should be valid JSON.
	var parsed any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
}

func TestSlimJSON_DeeplyNested(t *testing.T) {
	input := `{
		"title": "PR",
		"head": {
			"ref": "feature-branch",
			"repo": {
				"name": "my-repo",
				"html_url": "https://github.com/org/my-repo",
				"url": "https://api.github.com/repos/org/my-repo",
				"node_id": "R_abc",
				"owner": {
					"login": "org",
					"avatar_url": "https://avatars.githubusercontent.com/u/999",
					"node_id": "O_xyz"
				}
			}
		}
	}`
	got := SlimJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	head := parsed["head"].(map[string]any)
	repo := head["repo"].(map[string]any)
	owner := repo["owner"].(map[string]any)

	if owner["login"] != "org" {
		t.Error("expected deeply nested 'login' preserved")
	}
	if _, ok := owner["avatar_url"]; ok {
		t.Error("expected deeply nested 'avatar_url' removed")
	}
	if _, ok := owner["node_id"]; ok {
		t.Error("expected deeply nested 'node_id' removed")
	}
	if _, ok := repo["html_url"]; !ok {
		t.Error("expected repo 'html_url' preserved (has 'name')")
	}
	if _, ok := repo["url"]; ok {
		t.Error("expected repo 'url' removed")
	}
	if _, ok := repo["node_id"]; ok {
		t.Error("expected repo 'node_id' removed")
	}
}
