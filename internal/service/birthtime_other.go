//go:build !darwin

package service

import (
	"os"
	"time"
)

// fileBirthTime returns the file's modification time as a stand-in for a
// creation timestamp on platforms where this build doesn't read btime. The
// TUI uses IsNew only as a hint, so the degraded heuristic ("touched during
// the window" rather than "created during the window") is acceptable.
func fileBirthTime(info os.FileInfo) time.Time {
	return info.ModTime()
}
