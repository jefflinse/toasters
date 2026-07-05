package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jefflinse/toasters/internal/db"
)

// notesDirRelPath is the constant, job-relative location of job notes,
// mirroring internal/runtime/tools.go's notesDirRelPath. Duplicated here
// (rather than imported) because that constant is unexported and this read
// path has no CoreTools instance to resolve through — it works directly off
// the job's already-trusted WorkspaceDir from the store. See
// docs/kb-design.md's "Location".
const notesDirRelPath = ".toasters/notes"

// noteTimestampPrefixLen is the length of the fixed-format UTC timestamp
// internal/runtime's noteFilename stamps at the start of every note
// filename ("20060102-150405.000", 19 characters). Used by parseNoteSource
// to recover the stamped source segment for display.
const noteTimestampPrefixLen = len("20060102-150405.000")

// noteHexSuffixPattern matches the trailing "-<6hex>" random suffix minted
// by internal/runtime's randomHex6, so parseNoteSource can strip it off.
var noteHexSuffixPattern = regexp.MustCompile(`-[0-9a-f]{6}$`)

// jobWorkspaceDir resolves a job's workspace directory by id, the shared
// first step for both KnowledgeService methods. Returns an error wrapping
// ErrNotFound when the job (or its workspace) doesn't exist.
func (s *localKnowledgeService) jobWorkspaceDir(ctx context.Context, jobID string) (string, error) {
	if s.svc.cfg.Store == nil {
		return "", Unavailablef("store not configured")
	}
	job, err := s.svc.cfg.Store.GetJob(ctx, jobID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return "", fmt.Errorf("getting job %s: %w", jobID, ErrNotFound)
		}
		return "", fmt.Errorf("getting job %s: %w", jobID, err)
	}
	if job.WorkspaceDir == "" {
		return "", fmt.Errorf("job %s has no workspace directory: %w", jobID, ErrNotFound)
	}
	return job.WorkspaceDir, nil
}

// ListJobNotes returns metadata for every note under the job's
// .toasters/notes/ directory, newest first. A missing or empty notes
// directory (KB disabled, or no notes written yet) yields an empty slice,
// not an error — see docs/kb-design.md's kill-switch note.
func (s *localKnowledgeService) ListJobNotes(ctx context.Context, jobID string) ([]NoteMeta, error) {
	workDir, err := s.jobWorkspaceDir(ctx, jobID)
	if err != nil {
		return nil, err
	}

	notesDir := filepath.Join(workDir, filepath.FromSlash(notesDirRelPath))
	entries, err := os.ReadDir(notesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []NoteMeta{}, nil
		}
		return nil, fmt.Errorf("reading notes directory: %w", err)
	}

	notes := make([]NoteMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue // vanished between ReadDir and Info (rare race); skip it
		}
		id := strings.TrimSuffix(entry.Name(), ".md")
		title := readNoteTitle(filepath.Join(notesDir, entry.Name()))
		if title == "" {
			title = id
		}
		notes = append(notes, NoteMeta{
			ID:      id,
			Title:   title,
			Source:  parseNoteSource(id),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		})
	}

	sort.Slice(notes, func(i, j int) bool { return notes[i].ModTime.After(notes[j].ModTime) })
	return notes, nil
}

// ReadJobNote returns the full Markdown content of one note by id. id must
// be a bare filename component (no directory separators, no ".."); anything
// else is rejected before it ever reaches a path join, mirroring the
// traversal guard internal/runtime's resolvePath enforces on the write/tool
// side.
func (s *localKnowledgeService) ReadJobNote(ctx context.Context, jobID, id string) (string, error) {
	workDir, err := s.jobWorkspaceDir(ctx, jobID)
	if err != nil {
		return "", err
	}
	id = strings.TrimSuffix(id, ".md")
	if id == "" || filepath.Base(id) != id || strings.Contains(id, "..") {
		return "", Invalidf("invalid note id %q", id)
	}

	notesDir := filepath.Join(workDir, filepath.FromSlash(notesDirRelPath))
	path := filepath.Join(notesDir, id+".md")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("note %s: %w", id, ErrNotFound)
		}
		return "", fmt.Errorf("reading note: %w", err)
	}
	return string(content), nil
}

// readNoteTitle returns a note's title: the first non-empty line of its
// content with a leading Markdown heading marker stripped, mirroring
// internal/runtime/tools.go's deriveNoteTitle (duplicated rather than
// imported — that helper is unexported and this is a read-only display
// concern, not a tool-result formatting one). Only the first few KB are
// read since the title is always on the first line; returns "" on any
// read error or if the file has no non-empty line in that window.
func readNoteTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(line, "#"))
	}
	return ""
}

// parseNoteSource extracts the source segment from a note id
// (<timestamp>-<source>-<slug>-<6hex>, minted by internal/runtime's
// noteFilename). Both source and slug are sanitized tokens that may
// themselves contain internal dashes, so the boundary between them isn't
// always recoverable — this returns the token immediately after the
// timestamp when the id has the expected shape, and "" otherwise rather
// than guessing wrong. See docs/kb-design.md's "Write model: immutable
// entries".
func parseNoteSource(id string) string {
	if len(id) <= noteTimestampPrefixLen+1 || id[noteTimestampPrefixLen] != '-' {
		return ""
	}
	rest := id[noteTimestampPrefixLen+1:]
	loc := noteHexSuffixPattern.FindStringIndex(rest)
	if loc == nil {
		return ""
	}
	middle := rest[:loc[0]] // "source-slug"
	source, _, found := strings.Cut(middle, "-")
	if !found || source == "" {
		return ""
	}
	return source
}
