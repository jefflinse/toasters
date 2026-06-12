//go:build !unix

package runtime

import "os/exec"

// configureProcessTree is a no-op on platforms without Unix process groups.
// WaitDelay (set by the caller) still bounds the wait, so a surviving
// grandchild can't wedge the session — it just isn't killed.
func configureProcessTree(_ *exec.Cmd) {}
