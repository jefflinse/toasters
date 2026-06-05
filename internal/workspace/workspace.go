// Package workspace provides per-branch working-directory isolation for
// parallel graph execution. Each branch of a fan-out gets an independent copy
// of a base workspace so concurrent write-roles cannot clobber one another's
// files; the winning branch's changes are then promoted back over the base.
//
// Isolation is copy-based: a full recursive copy of the base (including its
// .git directory, so git-using roles keep working). This is always correct
// regardless of uncommitted changes in the base. A git-worktree fast path
// (cheaper, but only checks out committed state) is a possible future
// optimization.
package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// gitDir is the repository metadata directory, excluded from Promote so a
// branch's history never overwrites the base's.
const gitDir = ".git"

// Isolate creates n independent working directories seeded from base and
// returns their paths plus a cleanup function that removes them all. Each
// directory is a full recursive copy of base. The copies live under a single
// temp root; cleanup removes that root and is safe to call exactly once.
//
// For n <= 0 it returns no directories and a no-op cleanup. On any error it
// removes whatever it created before returning.
func Isolate(base string, n int) (dirs []string, cleanup func(), err error) {
	noop := func() {}
	if n <= 0 {
		return nil, noop, nil
	}
	info, err := os.Stat(base)
	if err != nil {
		return nil, noop, fmt.Errorf("workspace: stat base %q: %w", base, err)
	}
	if !info.IsDir() {
		return nil, noop, fmt.Errorf("workspace: base %q is not a directory", base)
	}

	root, err := os.MkdirTemp("", "toasters-fanout-")
	if err != nil {
		return nil, noop, fmt.Errorf("workspace: create temp root: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(root) }

	dirs = make([]string, 0, n)
	for i := range n {
		dst := filepath.Join(root, strconv.Itoa(i))
		if err := copyTree(base, dst, true); err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("workspace: copy base into branch %d: %w", i, err)
		}
		dirs = append(dirs, dst)
	}
	return dirs, cleanup, nil
}

// Promote makes base mirror winner's working tree, excluding the top-level
// .git directory of each: files present in winner are copied over base, and
// files in base that are absent from winner are removed. The winning branch's
// file changes thus appear in base as a working-tree delta, leaving base's
// own .git (history, index) untouched.
func Promote(winner, base string) error {
	keep, err := relSet(winner)
	if err != nil {
		return fmt.Errorf("workspace: scan winner %q: %w", winner, err)
	}

	// Remove base entries (outside .git) that the winner does not have.
	baseRel, err := relSet(base)
	if err != nil {
		return fmt.Errorf("workspace: scan base %q: %w", base, err)
	}
	for rel := range baseRel {
		if _, ok := keep[rel]; ok {
			continue
		}
		// RemoveAll is idempotent, so removing a parent before a stale child
		// is harmless.
		if err := os.RemoveAll(filepath.Join(base, rel)); err != nil {
			return fmt.Errorf("workspace: remove stale %q: %w", rel, err)
		}
	}

	// Copy winner's tree over base, creating and overwriting as needed.
	if err := copyTree(winner, base, false); err != nil {
		return fmt.Errorf("workspace: promote winner into base: %w", err)
	}
	return nil
}

// relSet returns the set of paths under root, relative to root, excluding the
// top-level .git directory and everything beneath it.
func relSet(root string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == gitDir || isUnder(rel, gitDir) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out[rel] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// copyTree recursively copies src into dst, creating dst if needed. When
// includeGit is false the top-level .git directory is skipped. Regular files,
// directories, and symlinks are handled; file modes are preserved.
func copyTree(src, dst string, includeGit bool) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, dirMode(d))
		}
		if !includeGit && (rel == gitDir || isUnder(rel, gitDir)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)

		switch {
		case d.IsDir():
			return os.MkdirAll(target, dirMode(d))
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target) // overwrite an existing link/file
			return os.Symlink(link, target)
		default:
			return copyFile(path, target, d)
		}
	})
}

// copyFile copies a single regular file from src to dst, preserving mode.
func copyFile(src, dst string, d os.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// dirMode returns a directory's permission bits, defaulting to 0o755 when the
// entry's info is unavailable.
func dirMode(d os.DirEntry) os.FileMode {
	info, err := d.Info()
	if err != nil {
		return 0o755
	}
	return info.Mode().Perm()
}

// isUnder reports whether rel is a path nested beneath dir (e.g. ".git/HEAD"
// is under ".git").
func isUnder(rel, dir string) bool {
	prefix := dir + string(os.PathSeparator)
	return len(rel) > len(prefix) && rel[:len(prefix)] == prefix
}
