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
	latest    time.Time      // 가장 최근 세션 활동 시각 — 정렬 전용(소문자라 JSON 직렬화 안 됨)
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
	Merged    *pullRequest  `json:"merged"` // base 브랜치로 머지 완료된 PR (있으면 머지됨)
	Sessions  []SessionView `json:"sessions"`
	latest    time.Time     // 이 워크트리의 가장 최근 세션 활동 — 정렬 전용(JSON 미직렬화)
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
		// repo의 origin 리모트에서 ADO 좌표(org/project/repo)를 도출한다.
		// 비-ADO(GitHub 등)면 isADO=false → 이 repo는 ADO 조회를 건너뛴다.
		var aOrg, aProj, aRepo string
		isADO := false
		if ra.isGit {
			aOrg, aProj, aRepo, isADO = adoRemote(ra.anyDir)
		}
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
		var latest time.Time // 이 repo의 모든 세션 중 가장 최근 활동 시각
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
			if ado != nil && isADO && w.Branch != "" && w.Branch != "(detached)" {
				// 캐시에서만 읽어 요청을 블로킹하지 않는다. 빈 값이면 다음 폴링에
				// 채워진다. track()으로 백그라운드 병렬 갱신 대상에 등록.
				wv.Pipeline = ado.cachedBuild(aOrg, aProj, aRepo, w.Branch)
				wv.PR = ado.cachedActivePR(aOrg, aProj, aRepo, w.Branch)
				if w.Branch != cfg.BaseBranch { // base 자신은 머지 판정 무의미
					wv.Merged = ado.cachedMergedPR(aOrg, aProj, aRepo, w.Branch, cfg.BaseBranch)
				}
				ado.track(aOrg, aProj, aRepo, w.Branch, cfg.BaseBranch)
			}
			var wtLatest time.Time // 이 워크트리의 가장 최근 세션 활동
			for _, s := range ss {
				wv.Sessions = append(wv.Sessions, sessionView(cfg, s, wv.Open))
				if s.LastTS.After(wtLatest) {
					wtLatest = s.LastTS
				}
			}
			wv.latest = wtLatest
			if wtLatest.After(latest) {
				latest = wtLatest // repo 단위 최근값으로 굴려 올림
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
			latest:    latest,
		})
	}
	// 가장 최근에 활동한 세션을 가진 프로젝트를 위로. 동률이면 이름순.
	sort.Slice(repos, func(i, j int) bool {
		if !repos[i].latest.Equal(repos[j].latest) {
			return repos[i].latest.After(repos[j].latest)
		}
		return repos[i].Name < repos[j].Name
	})

	return StateView{
		Repos:         repos,
		ADOConfigured: cfg.adoConfigured(),
		GeneratedAt:   time.Now().Format(time.RFC3339),
		ClaudeHome:    cfg.ClaudeHome,
	}
}

// isBoardWorktree reports whether path is one of the worktrees currently shown
// on the board. /api/open uses it to refuse running `code` on arbitrary paths.
func isBoardWorktree(cfg Config, ado *adoClient, path string) bool {
	if path == "" {
		return false
	}
	st := build(cfg, nil) // 경로 검증만 — ADO 조회 불필요
	for _, repo := range st.Repos {
		for _, wt := range repo.Worktrees {
			if wt.Path == path {
				return true
			}
		}
	}
	return false
}

func sessionView(cfg Config, s *session, wtOpen bool) SessionView {
	age := time.Since(s.LastTS)
	state := "idle"
	// waiting/recent는 VSCode-open을 요구하지 않는다 — 터미널에서 돌린 세션도
	// "Claude가 끝내고 내 입력을 기다리는" 상태로 잡아야 하기 때문. open은 표시(● VSCode)·
	// 정렬 가점용으로만 쓴다.
	switch {
	case age < time.Duration(cfg.RunningSec)*time.Second:
		state = "running"
	case s.LastRole == "assistant" && age < time.Duration(cfg.WaitingMin)*time.Minute:
		state = "waiting" // Claude가 막 응답을 끝냄 → 지금 당신 차례
	case s.LastRole == "assistant" && age < time.Duration(cfg.WaitingHrs)*time.Hour:
		state = "recent" // assistant로 끝났지만 한참 전 → 우선순위 낮은 대기
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
	case "running":
		return 0
	case "waiting":
		return 1
	case "recent":
		return 2
	default:
		return 3
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

func sortWorktrees(ws []WorktreeView) {
	// 가장 최근에 활동한 세션을 가진 워크트리를 위로. 동률이면 브랜치명순. (repo 정렬과 동일 기준)
	sort.Slice(ws, func(i, j int) bool {
		if !ws[i].latest.Equal(ws[j].latest) {
			return ws[i].latest.After(ws[j].latest)
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
