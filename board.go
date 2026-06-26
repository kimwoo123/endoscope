package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- view model returned as JSON to the browser ----

type StateView struct {
	Repos         []RepoView `json:"repos"`
	ADOConfigured bool       `json:"adoConfigured"`
	GeneratedAt   string     `json:"generatedAt"`
	ClaudeHome    string     `json:"claudeHome"`
}

type RepoView struct {
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	Worktrees []WorktreeView `json:"worktrees"`
}

type WorktreeView struct {
	Path      string        `json:"path"`
	Branch    string        `json:"branch"`
	Dirty     int           `json:"dirty"`
	Ahead     int           `json:"ahead"`
	Behind    int           `json:"behind"`
	Clean     bool          `json:"clean"`
	Open      bool          `json:"open"`
	IsGit     bool          `json:"isGit"`
	VSCodeURL string        `json:"vscodeUrl"`
	Pipeline  *pipeline     `json:"pipeline"`
	PR        *pullRequest  `json:"pr"`
	Sessions  []SessionView `json:"sessions"`
}

type SessionView struct {
	ID           string `json:"id"`
	Project      string `json:"project"` // viewer 딥링크 좌표
	File         string `json:"file"`    // viewer 딥링크 좌표
	Title        string `json:"title"`
	Prompt       string `json:"prompt"`
	State        string `json:"state"` // waiting | running | idle
	Open         bool   `json:"open"`
	Version      string `json:"version"`
	LastActivity string `json:"lastActivity"` // RFC3339
}

// build assembles the full board state from all data sources.
func build(cfg Config, ado *adoClient) StateView {
	sessions := loadSessions(cfg)
	open := openWorkspaces(cfg)

	sessByCwd := map[string][]*session{}
	for _, s := range sessions {
		sessByCwd[s.Cwd] = append(sessByCwd[s.Cwd], s)
	}

	// Collect candidate directories: every session cwd + configured extras.
	cwds := map[string]bool{}
	for c := range sessByCwd {
		cwds[c] = true
	}
	for _, r := range cfg.ExtraRepos {
		cwds[r] = true
	}

	// Group dirs into repos by shared git common-dir. Non-git dirs stand alone.
	type repoAccum struct {
		key     string
		anyDir  string
		isGit   bool
	}
	repoOf := map[string]*repoAccum{} // commonDir/standalone key -> accum
	order := []string{}
	for dir := range cwds {
		key := dir
		isGit := false
		if cd, ok := commonDir(dir); ok {
			key = cd
			isGit = true
		}
		if _, ok := repoOf[key]; !ok {
			repoOf[key] = &repoAccum{key: key, anyDir: dir, isGit: isGit}
			order = append(order, key)
		}
	}

	var repos []RepoView
	for _, key := range order {
		ra := repoOf[key]
		var wts []worktree
		if ra.isGit {
			wts = listWorktrees(ra.anyDir)
		} else {
			// Non-git directory: synthesize a single pseudo-worktree.
			br := ""
			if ss := sessByCwd[ra.anyDir]; len(ss) > 0 {
				br = ss[0].Branch
			}
			wts = []worktree{{Path: ra.anyDir, Branch: br, Clean: true}}
		}

		var wviews []WorktreeView
		for _, w := range wts {
			ss := sessByCwd[w.Path]
			// Only show worktrees that have sessions, plus extras the user asked for.
			if len(ss) == 0 && !contains(cfg.ExtraRepos, w.Path) {
				continue
			}
			wv := WorktreeView{
				Path:      w.Path,
				Branch:    w.Branch,
				Dirty:     w.Dirty,
				Ahead:     w.Ahead,
				Behind:    w.Behind,
				Clean:     w.Clean,
				IsGit:     ra.isGit,
				Open:      isOpen(w.Path, open),
				VSCodeURL: "vscode://file/" + w.Path,
			}
			if ado != nil && w.Branch != "" && w.Branch != "(detached)" {
				wv.Pipeline = ado.latestBuild(w.Branch)
				wv.PR = ado.activePR(w.Branch)
			}
			for _, s := range ss {
				wv.Sessions = append(wv.Sessions, sessionView(cfg, s, wv.Open))
			}
			sortSessions(wv.Sessions)
			wviews = append(wviews, wv)
		}
		if len(wviews) == 0 {
			continue
		}
		sortWorktrees(wviews)
		repos = append(repos, RepoView{
			Name:      filepath.Base(strings.TrimSuffix(strings.TrimSuffix(key, "/.git"), "/.bare")),
			Path:      ra.anyDir,
			Worktrees: wviews,
		})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })

	return StateView{
		Repos:         repos,
		ADOConfigured: cfg.adoConfigured(),
		GeneratedAt:   time.Now().Format(time.RFC3339),
		ClaudeHome:    cfg.ClaudeHome,
	}
}

func sessionView(cfg Config, s *session, wtOpen bool) SessionView {
	age := time.Since(s.LastTS)
	state := "idle"
	switch {
	case age < time.Duration(cfg.RunningSec)*time.Second:
		state = "running"
	case wtOpen && s.LastRole == "assistant" && age < time.Duration(cfg.WaitingHrs)*time.Hour:
		state = "waiting" // turn ended recently, VSCode open → waiting on the human
	}
	return SessionView{
		ID:           s.ID,
		Project:      s.Project,
		File:         s.File,
		Title:        s.Title,
		Prompt:       s.Prompt,
		State:        state,
		Open:         wtOpen,
		Version:      s.Version,
		LastActivity: s.LastTS.Format(time.RFC3339),
	}
}

// ---- IDE lock files ----

// openWorkspaces returns the set of workspace folder paths currently open in a
// VSCode window with the Claude extension connected.
func openWorkspaces(cfg Config) map[string]bool {
	out := map[string]bool{}
	dir := filepath.Join(cfg.ClaudeHome, "ide")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".lock" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var lock struct {
			WorkspaceFolders []string `json:"workspaceFolders"`
		}
		if json.Unmarshal(data, &lock) != nil {
			continue
		}
		for _, w := range lock.WorkspaceFolders {
			out[w] = true
		}
	}
	return out
}

func isOpen(path string, open map[string]bool) bool {
	if open[path] {
		return true
	}
	for w := range open {
		if strings.HasPrefix(path, w+"/") {
			return true
		}
	}
	return false
}

// ---- sorting helpers ----

func stateRank(s string) int {
	switch s {
	case "waiting":
		return 0
	case "running":
		return 1
	default:
		return 2
	}
}

func sortSessions(ss []SessionView) {
	sort.Slice(ss, func(i, j int) bool {
		if ri, rj := stateRank(ss[i].State), stateRank(ss[j].State); ri != rj {
			return ri < rj
		}
		return ss[i].LastActivity > ss[j].LastActivity
	})
}

func worktreeRank(w WorktreeView) int {
	best := 3
	for _, s := range w.Sessions {
		if r := stateRank(s.State); r < best {
			best = r
		}
	}
	return best
}

func sortWorktrees(ws []WorktreeView) {
	sort.Slice(ws, func(i, j int) bool {
		if ri, rj := worktreeRank(ws[i]), worktreeRank(ws[j]); ri != rj {
			return ri < rj
		}
		return ws[i].Branch < ws[j].Branch
	})
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
