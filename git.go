package main

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type worktree struct {
	Path   string
	Branch string
	Dirty  int
	Ahead  int
	Behind int
	Clean  bool
}

func runGit(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "color.ui=false"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// commonDir returns the shared .git dir for a path, used to group worktrees of
// the same repo together. ok=false means the path is not inside a git repo.
func commonDir(dir string) (string, bool) {
	out, ok := runGit(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// listWorktrees enumerates every worktree of the repo that `dir` belongs to.
func listWorktrees(dir string) []worktree {
	out, ok := runGit(dir, "worktree", "list", "--porcelain")
	if !ok {
		return nil
	}
	var wts []worktree
	var cur worktree
	flush := func() {
		if cur.Path != "" {
			cur.Clean = cur.Dirty == 0
			wts = append(wts, cur)
		}
		cur = worktree{}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = shortBranch(strings.TrimPrefix(line, "branch "))
		case line == "detached":
			cur.Branch = "(detached)"
		}
	}
	flush()
	for i := range wts {
		fillStatus(&wts[i])
	}
	return wts
}

var aheadRe = regexp.MustCompile(`ahead (\d+)`)
var behindRe = regexp.MustCompile(`behind (\d+)`)

func fillStatus(w *worktree) {
	out, ok := runGit(w.Path, "status", "-sb", "--porcelain=v1")
	if !ok {
		return
	}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if i == 0 {
			if m := aheadRe.FindStringSubmatch(line); m != nil {
				w.Ahead, _ = strconv.Atoi(m[1])
			}
			if m := behindRe.FindStringSubmatch(line); m != nil {
				w.Behind, _ = strconv.Atoi(m[1])
			}
			continue
		}
		if strings.TrimSpace(line) != "" {
			w.Dirty++
		}
	}
	w.Clean = w.Dirty == 0
}

func shortBranch(ref string) string {
	return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
}
