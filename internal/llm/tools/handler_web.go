package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jefflinse/toasters/internal/provider"
)

func handleFetchWebpage(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing fetch_webpage args: %w", err)
	}
	return fetchWebpage(args.URL)
}

func handleListDirectory(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing list_directory args: %w", err)
	}
	return listDirectory(args.Path, te.WorkspaceDir)
}
