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

// Azure DevOps lookups, keyed by branch and cached briefly so 5s board polls
// don't hammer the API.
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
	cfg  Config
	auth string
	mu   sync.Mutex
	bCache map[string]adoCacheEntry
	pCache map[string]adoCacheEntry
}

type adoCacheEntry struct {
	at   time.Time
	pipe *pipeline
	pr   *pullRequest
}

func newADO(cfg Config) *adoClient {
	return &adoClient{
		cfg:    cfg,
		auth:   "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+cfg.ADOPat)),
		bCache: map[string]adoCacheEntry{},
		pCache: map[string]adoCacheEntry{},
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

func (a *adoClient) base() string {
	return fmt.Sprintf("https://dev.azure.com/%s/%s/_apis",
		url.PathEscape(a.cfg.ADOOrg), url.PathEscape(a.cfg.ADOProject))
}

// latestBuild returns the most recent pipeline run for a branch.
func (a *adoClient) latestBuild(branch string) *pipeline {
	if branch == "" {
		return nil
	}
	a.mu.Lock()
	if e, ok := a.bCache[branch]; ok && time.Since(e.at) < adoTTL {
		a.mu.Unlock()
		return e.pipe
	}
	a.mu.Unlock()

	u := fmt.Sprintf("%s/build/builds?api-version=7.1&$top=1&queryOrder=queueTimeDescending&branchName=%s",
		a.base(), url.QueryEscape("refs/heads/"+branch))
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
	var p *pipeline
	if a.get(u, &resp) && len(resp.Value) > 0 {
		b := resp.Value[0]
		p = &pipeline{ID: b.ID, Number: b.BuildNumber, Status: b.Status, Result: b.Result, URL: b.Links.Web.Href}
	}
	a.mu.Lock()
	a.bCache[branch] = adoCacheEntry{at: time.Now(), pipe: p}
	a.mu.Unlock()
	return p
}

// activePR returns the active pull request whose source branch matches.
func (a *adoClient) activePR(branch string) *pullRequest {
	if branch == "" {
		return nil
	}
	a.mu.Lock()
	if e, ok := a.pCache[branch]; ok && time.Since(e.at) < adoTTL {
		a.mu.Unlock()
		return e.pr
	}
	a.mu.Unlock()

	u := fmt.Sprintf("%s/git/pullrequests?api-version=7.1&$top=1&searchCriteria.status=active&searchCriteria.sourceRefName=%s",
		a.base(), url.QueryEscape("refs/heads/"+branch))
	var resp struct {
		Value []struct {
			ID         int    `json:"pullRequestId"`
			Title      string `json:"title"`
			Repository struct {
				Name string `json:"name"`
			} `json:"repository"`
		} `json:"value"`
	}
	var pr *pullRequest
	if a.get(u, &resp) && len(resp.Value) > 0 {
		v := resp.Value[0]
		web := fmt.Sprintf("https://dev.azure.com/%s/%s/_git/%s/pullrequest/%d",
			url.PathEscape(a.cfg.ADOOrg), url.PathEscape(a.cfg.ADOProject), url.PathEscape(v.Repository.Name), v.ID)
		pr = &pullRequest{ID: v.ID, Title: v.Title, URL: web}
	}
	a.mu.Lock()
	a.pCache[branch] = adoCacheEntry{at: time.Now(), pr: pr}
	a.mu.Unlock()
	return pr
}
