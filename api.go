// API 응답 조립: 프로젝트/세션 목록, 세션 대화 렌더링, 경로 검증.
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// sessionPath는 project/file이 PROJECTS_DIR 바로 아래의 실제 파일일 때만 경로를 준다.
func sessionPath(project, file string) (string, bool) {
	if !validName(project) || !validName(file) {
		return "", false
	}
	p := filepath.Join(projectsDir, project, file)
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return "", false
	}
	return p, true
}

func validName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

type projectInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func listProjects() []projectInfo {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return []projectInfo{}
	}
	type pe struct {
		name string
		mt   time.Time
	}
	var ps []pe
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		ps = append(ps, pe{e.Name(), info.ModTime()})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].mt.After(ps[j].mt) })

	home, _ := os.UserHomeDir()
	user := filepath.Base(home)
	out := make([]projectInfo, 0, len(ps))
	for _, p := range ps {
		// 앞쪽 '-Users-<me>-' 류 접두어를 정리해 표시
		label := strings.ReplaceAll(p.name, "-Users-"+user+"-", "")
		label = strings.ReplaceAll(label, "-Users-"+user, "")
		if label == "" {
			label = p.name
		}
		out = append(out, projectInfo{p.name, label})
	}
	return out
}

type sessionInfo struct {
	File      string  `json:"file"`
	SessionID string  `json:"sessionId"`
	Title     string  `json:"title"`
	Label     *string `json:"label"`
	Mtime     string  `json:"mtime"`
}

// idOrFile은 내용에 sessionId가 없을 때 파일명(.jsonl 제거)을 ID로 쓴다.
func idOrFile(id, file string) string {
	if id != "" {
		return id
	}
	return strings.TrimSuffix(file, ".jsonl")
}

func listSessions(project string) []sessionInfo {
	out := []sessionInfo{}
	if !validName(project) {
		return out
	}
	dir := filepath.Join(projectsDir, project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	type fe struct {
		name string
		info os.FileInfo
	}
	var fs []fe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fs = append(fs, fe{e.Name(), info})
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].info.ModTime().After(fs[j].info.ModTime()) })

	labels := loadLabels()
	for _, f := range fs {
		// 목록용 title·ID는 보드와 같은 head/tail+캐시 distillation으로 얻는다.
		// 파일을 통째로 읽던 loadSession 호출을 제거 (큰 프로젝트 cold 로드 가속).
		// FileInfo를 넘겨 parseSessionCached의 중복 stat도 피한다.
		path := filepath.Join(dir, f.name)
		title, id := "", strings.TrimSuffix(f.name, ".jsonl")
		if s := parseSessionCached(path, f.info); s != nil {
			title, id = s.Title, s.ID
		}
		out = append(out, sessionInfo{
			File:      f.name,
			SessionID: id,
			Title:     title,
			Label:     labelPtr(labels, project+"/"+f.name),
			Mtime:     f.info.ModTime().Format("01-02 15:04"),
		})
	}
	saveTitleCache() // 새로 계산된 제목이 있으면 디스크에 영속
	return out
}

type tokenInfo struct {
	Output      int `json:"output"`      // 그 턴이 생성한 토큰 (메인 지표)
	Input       int `json:"input"`       // 신규(비캐시) 입력
	CacheRead   int `json:"cacheRead"`   // 캐시에서 읽은 양 (≈ 그 턴의 컨텍스트 크기)
	CacheCreate int `json:"cacheCreate"` // 캐시에 쓴 양
}

type message struct {
	Role   string     `json:"role"`
	Time   string     `json:"time"`
	Blocks []block    `json:"blocks"`
	Tokens *tokenInfo `json:"tokens,omitempty"` // 어시스턴트 턴에만
}

// JSON 숫자는 float64로 언마샬되므로 int로 변환
func intval(m map[string]any, k string) int {
	if v, ok := m[k].(float64); ok {
		return int(v)
	}
	return 0
}

type sessionResp struct {
	Title     string    `json:"title"`
	SessionID string    `json:"sessionId"`
	Label     *string   `json:"label"`
	Messages  []message `json:"messages"`
}

func renderSession(project, file string) sessionResp {
	resp := sessionResp{Messages: []message{}}
	path, ok := sessionPath(project, file)
	if !ok {
		return resp
	}
	lines := loadSession(path)

	// 단일 패스: 라인마다 iterBlocks를 '한 번만' 호출해 블록을 추출하고
	// (병목 3: 이전엔 두 번 호출), 동시에 tool_use_id -> 결과 매핑을 모은다.
	type parsedMsg struct {
		role, ts, id string
		blocks       []block
		usage        map[string]any // 그 턴의 usage (어시스턴트만, 없으면 nil)
	}
	var msgs []parsedMsg
	results := map[string]string{}
	for _, o := range lines {
		bs := iterBlocks(o)
		for _, b := range bs {
			if b.Kind == "result" && b.ref != "" {
				results[b.ref] = strings.TrimSpace(b.Text)
			}
		}
		t := str(o["type"])
		if t != "user" && t != "assistant" {
			continue
		}
		m, _ := o["message"].(map[string]any)
		id := str(m["id"])
		usage, _ := m["usage"].(map[string]any)
		// 한 턴(message.id)은 content 블록별로 여러 줄에 나뉘고 같은 usage를 공유한다.
		// 연속된 같은 id의 어시스턴트 라인을 한 메시지로 병합 → 턴당 말풍선·토큰 1개.
		if t == "assistant" && id != "" && len(msgs) > 0 {
			if last := &msgs[len(msgs)-1]; last.role == "assistant" && last.id == id {
				last.blocks = append(last.blocks, bs...)
				if last.usage == nil {
					last.usage = usage
				}
				continue
			}
		}
		msgs = append(msgs, parsedMsg{role: t, ts: str(o["timestamp"]), id: id, blocks: bs, usage: usage})
	}

	// 추출된 블록을 조립한다 (여기선 JSON 파싱 없음 — 결과 붙이고 비우기만).
	tokenSeen := map[string]bool{} // 같은 message.id의 토큰은 한 번만 집계
	for _, pm := range msgs {
		var blocks []block
		for _, b := range pm.blocks {
			if b.Kind == "text" {
				b.Text = stripWrappers(b.Text)
			} else {
				b.Text = strings.TrimSpace(b.Text)
			}
			if b.Kind == "result" {
				if _, paired := results[b.ref]; paired {
					continue // 결과는 대응하는 도구 호출 쪽에 붙여서 보여준다
				}
			}
			if b.Text == "" {
				continue
			}
			if b.Kind == "tool" {
				if r, paired := results[b.ref]; paired {
					b.Result = &r // 호출+결과를 한 묶음으로
				}
			}
			// Claude의 일반 텍스트만 마크다운 → HTML (내 메시지·thinking·도구는 원문 유지)
			if b.Kind == "text" && pm.role == "assistant" {
				b.HTML = renderMarkdown(b.Text)
			}
			blocks = append(blocks, b)
		}
		if len(blocks) > 0 {
			msg := message{Role: pm.role, Time: fmtTime(pm.ts), Blocks: blocks}
			if pm.role == "assistant" && pm.usage != nil && !(pm.id != "" && tokenSeen[pm.id]) {
				tokenSeen[pm.id] = true
				msg.Tokens = &tokenInfo{
					Output:      intval(pm.usage, "output_tokens"),
					Input:       intval(pm.usage, "input_tokens"),
					CacheRead:   intval(pm.usage, "cache_read_input_tokens"),
					CacheCreate: intval(pm.usage, "cache_creation_input_tokens"),
				}
			}
			resp.Messages = append(resp.Messages, msg)
		}
	}
	resp.Title = sessionTitle(lines)
	resp.SessionID = idOrFile(sessionID(lines), file)
	resp.Label = labelPtr(loadLabels(), project+"/"+file)
	return resp
}
