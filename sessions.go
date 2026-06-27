package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// session is the distilled state of one Claude Code session jsonl file.
type session struct {
	ID       string
	Project  string // encoded projects/ dir name — viewer 딥링크용
	File     string // jsonl 파일명 — viewer 딥링크용
	Cwd      string
	Branch   string
	Title    string // first user prompt, cropped — "what this session was doing"
	Prompt   string // last user prompt
	Version  string
	LastRole string // user | assistant — role of the latest main-chain entry
	LastTS   time.Time
}

type rawEntry struct {
	Type        string          `json:"type"`
	LastPrompt  string          `json:"lastPrompt"`
	Timestamp   string          `json:"timestamp"`
	Cwd         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	Version     string          `json:"version"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

// parse cache keyed by file path; reparses only when mtime changes.
var (
	sessCacheMu sync.Mutex
	sessCache   = map[string]sessCacheEntry{}
)

type sessCacheEntry struct {
	mtime time.Time
	size  int64
	sess  *session
}

// 첫 사용자 메시지(제목)는 한 번 쓰이면 불변이라, 파일이 자라도(mtime 변경) 무효화하지
// 않고 path를 키로 디스크에 영속 캐시한다 → 큰 파일도 제목 때문에 통째로 읽지 않는다.
var (
	titleCacheMu    sync.Mutex
	titleCache      = map[string]string{}
	titleCacheDirty bool
	titleCacheFile  string // main()에서 cfg.ClaudeHome 기준으로 채운다
)

// loadTitleCache는 시작 시 디스크의 제목 캐시를 메모리로 읽어들인다(없으면 무시).
func loadTitleCache() {
	if titleCacheFile == "" {
		return
	}
	data, err := os.ReadFile(titleCacheFile)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil || m == nil {
		return
	}
	titleCacheMu.Lock()
	titleCache = m
	titleCacheMu.Unlock()
}

// saveTitleCache는 새 제목이 추가됐을 때만 디스크에 원자적으로 쓴다.
func saveTitleCache() {
	titleCacheMu.Lock()
	if !titleCacheDirty || titleCacheFile == "" {
		titleCacheMu.Unlock()
		return
	}
	snap := make(map[string]string, len(titleCache))
	for k, v := range titleCache {
		snap[k] = v
	}
	titleCacheDirty = false
	titleCacheMu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	tmp := titleCacheFile + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, titleCacheFile)
	}
}

// loadSessions scans CLAUDE_HOME/projects/*/*.jsonl and returns one distilled
// session per file that has been active within MaxAgeHours.
func loadSessions(cfg Config) []*session {
	root := filepath.Join(cfg.ClaudeHome, "projects")
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(cfg.MaxAgeHours) * time.Hour)

	var out []*session
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			// mtime 사전 필터: 마지막 대화 시각은 파일 mtime을 넘을 수 없으므로,
			// mtime이 cutoff보다 오래면 파싱(=파일 읽기) 없이 건너뛴다.
			info, err := f.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			path := filepath.Join(root, d.Name(), f.Name())
			s := parseSessionCached(path)
			if s == nil || s.LastTS.Before(cutoff) || s.Cwd == "" {
				continue
			}
			out = append(out, s)
		}
	}
	saveTitleCache() // 이번 스캔에서 새로 계산된 제목이 있으면 디스크에 영속
	return out
}

func parseSessionCached(path string) *session {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	sessCacheMu.Lock()
	if e, ok := sessCache[path]; ok && e.mtime.Equal(info.ModTime()) && e.size == info.Size() {
		sessCacheMu.Unlock()
		return e.sess
	}
	sessCacheMu.Unlock()

	s := parseSession(path, info.Size())
	sessCacheMu.Lock()
	sessCache[path] = sessCacheEntry{mtime: info.ModTime(), size: info.Size(), sess: s}
	sessCacheMu.Unlock()
	return s
}

// parseSession distills one session without reading the whole file: the title
// (immutable) comes from a disk-cached head read, and the mutable last-* fields
// from an adaptive tail read. A 20MB file thus costs ~head(once)+tail, not 20MB.
func parseSession(path string, size int64) *session {
	s := &session{
		ID:      trimExt(filepath.Base(path)),
		File:    filepath.Base(path),
		Project: filepath.Base(filepath.Dir(path)),
	}
	s.Title = headTitle(path, size) // 불변 — 디스크 캐시 우선
	parseTail(path, size, s)        // 가변 — 꼬리만 읽어 LastTS/Role/Prompt/Cwd/Branch 채움
	if s.LastTS.IsZero() {
		return nil
	}
	return s
}

// headTitle returns the first human-authored prompt (cropped). It's immutable
// per file, so it's cached permanently on disk keyed by path.
func headTitle(path string, size int64) string {
	titleCacheMu.Lock()
	if t, ok := titleCache[path]; ok {
		titleCacheMu.Unlock()
		return t
	}
	titleCacheMu.Unlock()

	t := readHeadTitle(path, size)

	titleCacheMu.Lock()
	titleCache[path] = t
	titleCacheDirty = true
	titleCacheMu.Unlock()
	return t
}

// readHeadTitle reads only the head of the file, growing the window until it
// finds the first meaningful user text (almost always near the very top).
func readHeadTitle(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const maxHead = 1 << 20 // 1MB 상한 — 이 안에 첫 user가 없으면 포기
	window := int64(128 * 1024)
	for {
		whole := window >= size
		if whole {
			window = size
		}
		buf := make([]byte, window)
		n, _ := f.ReadAt(buf, 0)
		lines := splitLines(buf[:n], false, !whole) // 끝까지 안 읽었으면 마지막(미완) 줄 버림
		if t := firstUserTitle(lines); t != "" {
			return t
		}
		if whole || window >= maxHead {
			return ""
		}
		window *= 2
	}
}

// firstUserTitle returns the title from the first meaningful, non-sidechain
// user message among the given lines, or "" if none.
func firstUserTitle(lines [][]byte) string {
	for _, line := range lines {
		var e rawEntry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if e.Type != "user" || e.IsSidechain {
			continue
		}
		if txt := meaningfulUserText(e.Message); txt != "" {
			return titleFromText(txt)
		}
	}
	return ""
}

// parseTail reads only the tail of the file to fill the mutable fields. The
// window grows until it captures the latest user/assistant entry (entries can
// exceed 1MB), since that carries LastTS/Cwd/Branch.
func parseTail(path string, size int64, s *session) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	window := int64(256 * 1024)
	for {
		whole := window >= size
		if whole {
			window = size
		}
		off := size - window
		buf := make([]byte, window)
		n, _ := f.ReadAt(buf, off)
		lines := splitLines(buf[:n], !whole, false) // 시작이 줄 중간이면 첫(미완) 줄 버림
		applyTail(lines, s)
		if !s.LastTS.IsZero() || whole {
			return
		}
		window *= 2 // 마지막 user/assistant를 못 잡았으면 윈도우 확대
	}
}

// applyTail folds the tail lines into s using the same rules as the original
// chronological scan. It's monotonic (only newer ts wins), so re-applying on a
// grown, overlapping window is safe.
func applyTail(lines [][]byte, s *session) {
	for _, line := range lines {
		var e rawEntry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch e.Type {
		case "last-prompt":
			if e.LastPrompt != "" {
				s.Prompt = e.LastPrompt
			}
		case "user", "assistant":
			if e.IsSidechain {
				continue // subagent traffic, not the user-facing turn
			}
			ts := parseTS(e.Timestamp)
			if ts.IsZero() || !ts.After(s.LastTS) {
				continue
			}
			s.LastTS = ts
			s.LastRole = e.Type
			if e.Cwd != "" {
				s.Cwd = e.Cwd
			}
			if e.GitBranch != "" {
				s.Branch = e.GitBranch
			}
			if e.Version != "" {
				s.Version = e.Version
			}
		}
	}
}

// splitLines splits a byte window into non-empty trimmed lines, optionally
// dropping the first/last line when the window starts/ends mid-line.
func splitLines(buf []byte, dropFirst, dropLast bool) [][]byte {
	parts := bytes.Split(buf, []byte{'\n'})
	if dropLast && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	if dropFirst && len(parts) > 0 {
		parts = parts[1:]
	}
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if p = bytes.TrimSpace(p); len(p) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// meaningfulUserText extracts the first human-authored text from a user
// message, skipping harness-injected content (IDE events, slash commands,
// system reminders) and tool-result-only turns.
func meaningfulUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	// content is either a plain string or an array of typed blocks. A single
	// user turn can carry several text blocks (e.g. an injected <ide_selection>
	// block followed by the real prompt), so skip noise and take the first
	// genuine text rather than bailing on the first block.
	var str string
	if json.Unmarshal(m.Content, &str) == nil {
		if str = strings.TrimSpace(str); str != "" && !isInjected(str) {
			return str
		}
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if t := strings.TrimSpace(b.Text); t != "" && !isInjected(t) {
			return t
		}
	}
	return ""
}

// isInjected reports whether text is harness-injected noise rather than a
// human instruction.
func isInjected(s string) bool {
	for _, p := range []string{
		"<ide_", "<command-", "<system-reminder>", "<local-command",
		"<user-", "<bash-", "Caveat:",
	} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// titleFromText condenses arbitrary message text into a one-line title.
func titleFromText(s string) string {
	s = strings.Join(strings.Fields(s), " ") // collapse newlines/runs of spaces
	const max = 80
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func trimExt(name string) string {
	return name[:len(name)-len(filepath.Ext(name))]
}
