package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jefflinse/toasters/internal/llm"
)

func handleFetchWebpage(_ context.Context, te *ToolExecutor, call llm.ToolCall) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("parsing fetch_webpage args: %w", err)
	}
	return fetchWebpage(args.URL)
}

func handleListDirectory(_ context.Context, te *ToolExecutor, call llm.ToolCall) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("parsing list_directory args: %w", err)
	}
	return listDirectory(args.Path, te.WorkspaceDir)
}
