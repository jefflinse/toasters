package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// AvailableTools is the set of tools exposed to the LLM.
var AvailableTools = []Tool{
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "fetch_webpage",
			Description: "Fetches the content of a web page and returns it as plain text.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL of the web page to fetch.",
					},
				},
				"required": []string{"url"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "list_directory",
			Description: "Lists the contents of a local directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "The absolute or relative path to the directory.",
					},
				},
				"required": []string{"path"},
			},
		},
	},
}

// ExecuteTool dispatches a tool call to the appropriate handler and returns
// the result as plain text.
func ExecuteTool(call ToolCall) (string, error) {
	switch call.Function.Name {
	case "fetch_webpage":
		var args struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing fetch_webpage args: %w", err)
		}
		return fetchWebpage(args.URL)
	case "list_directory":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing list_directory args: %w", err)
		}
		return listDirectory(args.Path)
	default:
		return "", fmt.Errorf("unknown tool: %s", call.Function.Name)
	}
}

// fetchWebpage retrieves a URL and returns its content as plain text.
func fetchWebpage(url string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "toasters/0.1")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d fetching %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response body: %w", err)
	}

	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}

	var parts []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		// Skip subtrees rooted at script, style, or head nodes.
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head":
				return
			}
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				parts = append(parts, text)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	result := strings.Join(parts, " ")

	// Collapse runs of whitespace and newlines.
	wsRe := regexp.MustCompile(`\s+`)
	result = wsRe.ReplaceAllString(result, " ")
	result = strings.TrimSpace(result)

	const maxLen = 8000
	if len(result) > maxLen {
		result = result[:maxLen] + "...[truncated]"
	}

	return result, nil
}

// listDirectory returns a formatted listing of the directory at path.
func listDirectory(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("reading directory %s: %w", path, err)
	}

	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			lines = append(lines, fmt.Sprintf("[dir]  %s/", entry.Name()))
		} else {
			info, err := entry.Info()
			if err != nil {
				return "", fmt.Errorf("getting info for %s: %w", entry.Name(), err)
			}
			lines = append(lines, fmt.Sprintf("[file] %s  (%d bytes)", entry.Name(), info.Size()))
		}
	}

	return strings.Join(lines, "\n"), nil
}
