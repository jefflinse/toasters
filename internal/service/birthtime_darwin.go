package service

import (
	"os"
	"syscall"
	"time"
)

// fileBirthTime returns the file's creation timestamp. macOS exposes btime
// via Stat_t.Birthtimespec; if the underlying Sys() isn't a Stat_t (e.g. a
// synthetic FileInfo in tests), fall back to the modification time.
func fileBirthTime(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	}
	return info.ModTime()
}
