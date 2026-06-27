package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Azure DevOps lookups. Every query is scoped to a specific repository whose
// org/project/repo are derived from that repo's git remote (see adoRemote), so
// branches with the same name across repos/projects never cross-match. Results
// are cached briefly so 5s board polls don't hammer the API.
const adoTTL = 20 * time.Second

type pipeline struct {
	ID     int    `json:"id"`
	Number string `json:"number"`
	Status string `json:"status"` // notStarted | inProgress | completed
	Result string `json:"result"` // succeeded | failed | canceled | partiallySucceeded
	URL    string `json:"url"`
}

type pullRequest struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type adoClient struct {
	auth    string
	mu      sync.Mutex
	idCache map[string]string        // org/project/repo -> repository GUID
	bCache  map[string]adoCacheEntry // org/project/repo/branch -> build
	pCache  map[string]adoCacheEntry // org/project/repo/branch -> active PR
	mCache  map[string]adoCacheEntry // org/project/repo/branch->base -> merged PR

	targets map[string]adoTarget // 보드가 본 조회 대상 — 백그라운드 갱신용
	nudge   chan struct{}        // 새 대상 등장 시 즉시 갱신 신호(버퍼 1)
}

type adoCacheEntry struct {
	at     time.Time
	pipe   *pipeline
	pr     *pullRequest
	merged *pullRequest
}

// adoTarget은 한 워크트리의 ADO 좌표. 요청 경로(build)가 등록하고,
// refreshLoop이 이 목록을 병렬로 갱신해 캐시를 채운다.
type adoTarget struct {
	org, project, repo, branch, base string
	wantMerged                       bool // branch != base 일 때만 머지 조회
}

func newADO(cfg Config) *adoClient {
	return &adoClient{
		auth:    "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+cfg.ADOPat)),
		idCache: map[string]string{},
		bCache:  map[string]adoCacheEntry{},
		pCache:  map[string]adoCacheEntry{},
		mCache:  map[string]adoCacheEntry{},
		targets: map[string]adoTarget{},
		nudge:   make(chan struct{}, 1),
	}
}

func (a *adoClient) get(rawurl string, v any) bool {
	req, err := http.NewRequest("GET", rawurl, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", a.auth)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return false
	}
	return json.NewDecoder(resp.Body).Decode(v) == nil
}

func (a *adoClient) base(org, project string) string {
	return fmt.Sprintf("https://dev.azure.com/%s/%s/_apis",
		url.PathEscape(org), url.PathEscape(project))
}

// repoID resolves a repository's stable GUID, used to scope build/PR queries to
// exactly this repo. Cached forever (repo IDs don't change); only successes are
// cached so a transient failure retries on the next poll.
func (a *adoClient) repoID(org, project, repo string) (string, bool) {
	key := org + "/" + project + "/" + repo
	a.mu.Lock()
	id, ok := a.idCache[key]
	a.mu.Unlock()
	if ok {
		return id, true
	}
	u := fmt.Sprintf("%s/git/repositories/%s?api-version=7.1",
		a.base(org, project), url.PathEscape(repo))
	var resp struct {
		ID string `json:"id"`
	}
	if !a.get(u, &resp) || resp.ID == "" {
		return "", false
	}
	a.mu.Lock()
	a.idCache[key] = resp.ID
	a.mu.Unlock()
	return resp.ID, true
}

func (a *adoClient) prWebURL(org, project, repo string, id int) string {
	return fmt.Sprintf("https://dev.azure.com/%s/%s/_git/%s/pullrequest/%d",
		url.PathEscape(org), url.PathEscape(project), url.PathEscape(repo), id)
}

// latestBuild returns the most recent pipeline run for a branch in this repo.
func (a *adoClient) latestBuild(org, project, repo, branch string) *pipeline {
	if branch == "" {
		return nil
	}
	key := org + "/" + project + "/" + repo + "/" + branch
	a.mu.Lock()
	if e, ok := a.bCache[key]; ok && time.Since(e.at) < adoTTL {
		a.mu.Unlock()
		return e.pipe
	}
	a.mu.Unlock()

	var p *pipeline
	if id, ok := a.repoID(org, project, repo); ok {
		u := fmt.Sprintf("%s/build/builds?api-version=7.1&$top=1&queryOrder=queueTimeDescending&repositoryId=%s&repositoryType=TfsGit&branchName=%s",
			a.base(org, project), url.QueryEscape(id), url.QueryEscape("refs/heads/"+branch))
		var resp struct {
			Value []struct {
				ID          int    `json:"id"`
				BuildNumber string `json:"buildNumber"`
				Status      string `json:"status"`
				Result      string `json:"result"`
				Links       struct {
					Web struct {
						Href string `json:"href"`
					} `json:"web"`
				} `json:"_links"`
			} `json:"value"`
		}
		if a.get(u, &resp) && len(resp.Value) > 0 {
			b := resp.Value[0]
			p = &pipeline{ID: b.ID, Number: b.BuildNumber, Status: b.Status, Result: b.Result, URL: b.Links.Web.Href}
		}
	}
	a.mu.Lock()
	a.bCache[key] = adoCacheEntry{at: time.Now(), pipe: p}
	a.mu.Unlock()
	return p
}

// activePR returns the active pull request whose source branch matches, in this repo.
func (a *adoClient) activePR(org, project, repo, branch string) *pullRequest {
	if branch == "" {
		return nil
	}
	key := org + "/" + project + "/" + repo + "/" + branch
	a.mu.Lock()
	if e, ok := a.pCache[key]; ok && time.Since(e.at) < adoTTL {
		a.mu.Unlock()
		return e.pr
	}
	a.mu.Unlock()

	var pr *pullRequest
	if id, ok := a.repoID(org, project, repo); ok {
		u := fmt.Sprintf("%s/git/repositories/%s/pullrequests?api-version=7.1&$top=1&searchCriteria.status=active&searchCriteria.sourceRefName=%s",
			a.base(org, project), url.PathEscape(id), url.QueryEscape("refs/heads/"+branch))
		var resp struct {
			Value []struct {
				ID    int    `json:"pullRequestId"`
				Title string `json:"title"`
			} `json:"value"`
		}
		if a.get(u, &resp) && len(resp.Value) > 0 {
			v := resp.Value[0]
			pr = &pullRequest{ID: v.ID, Title: v.Title, URL: a.prWebURL(org, project, repo, v.ID)}
		}
	}
	a.mu.Lock()
	a.pCache[key] = adoCacheEntry{at: time.Now(), pr: pr}
	a.mu.Unlock()
	return pr
}

// mergedPR returns the most recent completed PR from branch into base in this
// repo. A non-nil result means this branch's work was merged into base
// (squash·rebase·merge 무관 — PR 완료가 곧 머지).
func (a *adoClient) mergedPR(org, project, repo, branch, base string) *pullRequest {
	if branch == "" {
		return nil
	}
	key := org + "/" + project + "/" + repo + "/" + branch + "->" + base
	a.mu.Lock()
	if e, ok := a.mCache[key]; ok && time.Since(e.at) < adoTTL {
		a.mu.Unlock()
		return e.merged
	}
	a.mu.Unlock()

	var pr *pullRequest
	if id, ok := a.repoID(org, project, repo); ok {
		u := fmt.Sprintf("%s/git/repositories/%s/pullrequests?api-version=7.1&$top=1&searchCriteria.status=completed&searchCriteria.sourceRefName=%s&searchCriteria.targetRefName=%s",
			a.base(org, project), url.PathEscape(id), url.QueryEscape("refs/heads/"+branch), url.QueryEscape("refs/heads/"+base))
		var resp struct {
			Value []struct {
				ID    int    `json:"pullRequestId"`
				Title string `json:"title"`
			} `json:"value"`
		}
		if a.get(u, &resp) && len(resp.Value) > 0 {
			v := resp.Value[0]
			pr = &pullRequest{ID: v.ID, Title: v.Title, URL: a.prWebURL(org, project, repo, v.ID)}
		}
	}
	a.mu.Lock()
	a.mCache[key] = adoCacheEntry{at: time.Now(), merged: pr}
	a.mu.Unlock()
	return pr
}

// ---- 비차단 캐시 읽기 + 백그라운드 병렬 갱신 ----
//
// 요청 경로(build)는 아래 cached* 로 캐시에서만 읽어 절대 HTTP를 기다리지 않는다.
// 동시에 track()으로 좌표를 등록하면 refreshLoop이 그 대상들을 병렬로 갱신해
// 캐시를 채운다 → 첫 로드는 즉시(뱃지 없이) 뜨고, 뱃지는 다음 폴링에 나타난다.

func (a *adoClient) cachedBuild(org, project, repo, branch string) *pipeline {
	key := org + "/" + project + "/" + repo + "/" + branch
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bCache[key].pipe
}

func (a *adoClient) cachedActivePR(org, project, repo, branch string) *pullRequest {
	key := org + "/" + project + "/" + repo + "/" + branch
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pCache[key].pr
}

func (a *adoClient) cachedMergedPR(org, project, repo, branch, base string) *pullRequest {
	key := org + "/" + project + "/" + repo + "/" + branch + "->" + base
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mCache[key].merged
}

// track registers a worktree's ADO coordinates for background refresh. A newly
// seen target nudges the loop so its badges appear on the next poll, not 15s later.
func (a *adoClient) track(org, project, repo, branch, base string) {
	key := org + "/" + project + "/" + repo + "/" + branch + "->" + base
	a.mu.Lock()
	_, seen := a.targets[key]
	if !seen {
		a.targets[key] = adoTarget{org, project, repo, branch, base, branch != base}
	}
	a.mu.Unlock()
	if !seen {
		select {
		case a.nudge <- struct{}{}:
		default: // 이미 대기 중인 신호가 있으면 그걸로 충분
		}
	}
}

// refreshLoop keeps the ADO cache warm: refresh all tracked targets, then wait
// for a tick or a nudge (new target). Started once from main().
func (a *adoClient) refreshLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		a.refreshAll()
		select {
		case <-ticker.C:
		case <-a.nudge:
		}
	}
}

// refreshAll fetches every tracked target concurrently (capped). The underlying
// latestBuild/activePR/mergedPR only hit the network when their cache is stale,
// so this is cheap once warm.
func (a *adoClient) refreshAll() {
	a.mu.Lock()
	targets := make([]adoTarget, 0, len(a.targets))
	for _, t := range a.targets {
		targets = append(targets, t)
	}
	a.mu.Unlock()

	const maxConcurrent = 8
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, t := range targets {
		t := t
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			a.latestBuild(t.org, t.project, t.repo, t.branch)
			a.activePR(t.org, t.project, t.repo, t.branch)
			if t.wantMerged {
				a.mergedPR(t.org, t.project, t.repo, t.branch, t.base)
			}
		}()
	}
	wg.Wait()
}
