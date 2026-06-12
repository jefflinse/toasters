package operator

import (
	"os"
	"strings"
)

// contractHome replaces the user's home directory prefix with "~/" for
// shorter, more readable paths in tool output. If the home directory
// cannot be determined or the path is not under it, the path is returned
// unchanged.
func contractHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home+"/") {
		return "~/" + path[len(home)+1:]
	}
	if path == home {
		return "~"
	}
	return path
}
