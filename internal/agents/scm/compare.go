package scm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ── Ref comparison ──────────────────────────────────────────────────

// CompareFile is one file that differs between two refs. Additions and
// Deletions follow the same convention as CommitFile: -1 means binary,
// which git prints as "-" and which is not "changed by zero lines".
type CompareFile struct {
	Path      string `json:"path"`
	OrigPath  string `json:"orig_path,omitempty"` // rename/copy source
	Status    string `json:"status"`              // A/M/D/R/C...
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// CompareResult is everything the Compare tab needs for one base↔head
// pick: the changed files, how far the two refs have drifted apart, and
// where they last agreed.
type CompareResult struct {
	Base string `json:"base"`
	Head string `json:"head"`
	// MergeBase is empty when the two refs share no history at all —
	// that is a legitimate comparison (two imported trees), not an error.
	MergeBase string `json:"merge_base,omitempty"`
	// Ahead/Behind are counted from head's point of view: commits head
	// has that base does not, and the other way round.
	Ahead  int           `json:"ahead"`
	Behind int           `json:"behind"`
	Files  []CompareFile `json:"files"`
}

// CompareRefs diffs two refs the way a review tool does.
//
// threeDot picks which question is being asked. `base..head` is the raw
// difference between two trees — it also reports, as removals, work that
// base gained meanwhile. `base...head` diffs from the merge base, so it
// shows only what head itself added; that is what "what does this branch
// change" means to a reviewer, and why the panel defaults to it.
//
// Two passes over git are needed because --name-status and --numstat
// cannot be asked for in one invocation: the first gives the status
// letter and the rename source, the second the line counts.
func CompareRefs(ctx context.Context, dir, base, head string, threeDot bool) (CompareResult, error) {
	base, head = strings.TrimSpace(base), strings.TrimSpace(head)
	if err := validRefName(base); err != nil {
		return CompareResult{}, fmt.Errorf("base: %w", err)
	}
	if err := validRefName(head); err != nil {
		return CompareResult{}, fmt.Errorf("head: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()

	sep := ".."
	if threeDot {
		sep = "..."
	}
	rng := base + sep + head

	res := CompareResult{Base: base, Head: head, Files: []CompareFile{}}
	out, err := run(ctx, dir, "diff", "--no-color", "--name-status", "-z", "--find-renames", rng)
	if err != nil {
		return CompareResult{}, err
	}
	res.Files = parseCompareNameStatusZ(out)

	// Counts are advisory: a file list without ± is still usable, a 400
	// instead of it is not. Same call for both passes so rename detection
	// keys the numbers onto the same paths.
	if nout, nerr := run(ctx, dir, "diff", "--no-color", "--numstat", "-z", "--find-renames", rng); nerr == nil {
		counts := parseNumstatZ(nout)
		for i := range res.Files {
			if n, ok := counts[res.Files[i].Path]; ok {
				res.Files[i].Additions, res.Files[i].Deletions = n[0], n[1]
			}
		}
	}

	// `rev-list --left-right --count A...B` prints "<left>\t<right>":
	// commits only on base, then commits only on head.
	if rl, rerr := run(ctx, dir, "rev-list", "--left-right", "--count", base+"..."+head); rerr == nil {
		if f := strings.Fields(rl); len(f) == 2 {
			res.Behind, _ = strconv.Atoi(f[0])
			res.Ahead, _ = strconv.Atoi(f[1])
		}
	}
	// No merge base is not a failure — unrelated histories simply have
	// none, and the header says so instead of the request erroring out.
	if mb, mberr := MergeBase(ctx, dir, base, head); mberr == nil {
		res.MergeBase = mb
	}
	return res, nil
}

// MergeBase returns the sha where two refs last agreed. Refs that share
// no history make git exit non-zero; that is reported as an error and the
// callers treat it as "no merge base" rather than as a broken request.
func MergeBase(ctx context.Context, dir, base, head string) (string, error) {
	if err := validRefName(base); err != nil {
		return "", fmt.Errorf("base: %w", err)
	}
	if err := validRefName(head); err != nil {
		return "", fmt.Errorf("head: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	out, err := run(ctx, dir, "merge-base", base, head)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// parseCompareNameStatusZ decodes `git diff --name-status -z`. Each entry
// is the status field followed by the path; a rename or copy carries two
// paths (source then destination).
func parseCompareNameStatusZ(s string) []CompareFile {
	fields := strings.Split(s, "\x00")
	out := []CompareFile{}
	for i := 0; i < len(fields); i++ {
		st := strings.TrimSpace(fields[i])
		if st == "" {
			continue
		}
		code := st[:1]
		if code == "R" || code == "C" {
			if i+2 < len(fields) {
				out = append(out, CompareFile{Path: fields[i+2], OrigPath: fields[i+1], Status: code})
				i += 2
			}
			continue
		}
		if i+1 < len(fields) {
			out = append(out, CompareFile{Path: fields[i+1], Status: code})
			i++
		}
	}
	return out
}

// parseNumstatZ maps path -> [additions, deletions] for a `--numstat -z`
// run, keyed by the DESTINATION path so it lines up with the name-status
// pass. Unlike the newline form, -z writes a rename as the two counts
// followed by two separate NUL-terminated paths.
func parseNumstatZ(s string) map[string][2]int {
	res := map[string][2]int{}
	fields := strings.Split(s, "\x00")
	parse := func(v string) int {
		if v == "-" {
			return -1 // binary
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0
		}
		return n
	}
	for i := 0; i < len(fields); i++ {
		f := strings.Split(fields[i], "\t")
		if len(f) < 3 {
			continue
		}
		add, del := parse(f[0]), parse(f[1])
		if f[2] == "" {
			// Rename: the empty path slot means the two paths follow as
			// their own fields. Credit the counts to the new name.
			if i+2 < len(fields) {
				res[fields[i+2]] = [2]int{add, del}
				i += 2
			}
			continue
		}
		res[f[2]] = [2]int{add, del}
	}
	return res
}

// ── Rollback ────────────────────────────────────────────────────────

// RestorePaths puts the given paths back to their content at ref
// (`git checkout <ref> -- <paths>`) — the per-file rollback. DESTRUCTIVE
// for those paths: whatever the working tree held is overwritten and the
// index is updated too, so the caller must confirm first.
func RestorePaths(ctx context.Context, dir, ref string, paths []string) error {
	if len(paths) == 0 {
		return errors.New("no paths to restore")
	}
	ref = strings.TrimSpace(ref)
	if err := validRefName(ref); err != nil {
		return fmt.Errorf("ref: %w", err)
	}
	for _, p := range paths {
		if _, err := safeRepoJoin(dir, p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	args := append([]string{"checkout", ref, "--"}, paths...)
	_, err := runIndex(ctx, dir, args...)
	return err
}

// RevertCommit applies the inverse of a commit. noCommit (-n) leaves the
// inverse staged instead of committing it, which is how you revert a few
// commits into one.
//
// Returns git's stdout so the caller can show what happened; on conflict
// that stdout is the only place the conflicting files are named.
func RevertCommit(ctx context.Context, dir, sha string, noCommit bool) (string, error) {
	sha = strings.TrimSpace(sha)
	if err := validRefName(sha); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	args := []string{"revert", "--no-edit"}
	if noCommit {
		args = append(args, "-n")
	}
	args = append(args, sha)
	out, err := runIndex(ctx, dir, args...)
	if err != nil {
		// git splits a failed revert across both streams: "could not
		// revert …" on stderr, the CONFLICT lines on stdout. GitError
		// only carries stderr, so a caller shown that alone is told the
		// revert failed without being told which file is in the way.
		if msg := strings.TrimSpace(out); msg != "" {
			return out, fmt.Errorf("%w\n%s", err, msg)
		}
		return out, err
	}
	return out, nil
}

// resetModes is the allowlist for ResetTo. `merge` and `keep` are left
// out on purpose: they are conditional resets whose refusals need a UI
// that explains them, and the panel offers none.
var resetModes = map[string]bool{"soft": true, "mixed": true, "hard": true}

// ResetTo moves the current branch to ref. DESTRUCTIVE in mode "hard" —
// the working tree is overwritten and uncommitted work is gone with no
// reflog entry to recover it from, so the caller must confirm first.
func ResetTo(ctx context.Context, dir, ref, mode string) error {
	ref = strings.TrimSpace(ref)
	if err := validRefName(ref); err != nil {
		return fmt.Errorf("ref: %w", err)
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "mixed"
	}
	if !resetModes[mode] {
		return fmt.Errorf("reset mode %q is not one of soft, mixed, hard", mode)
	}
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	_, err := runIndex(ctx, dir, "reset", "--"+mode, ref)
	return err
}
