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
	ID        string
	Cwd       string
	Branch    string
	Title     string // first user prompt, cropped — "what this session was doing"
	Prompt    string // last user prompt
	Version   string
	LastRole  string // user | assistant — role of the latest main-chain entry
	LastTS    time.Time
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
			path := filepath.Join(root, d.Name(), f.Name())
			s := parseSessionCached(path)
			if s == nil || s.LastTS.Before(cutoff) || s.Cwd == "" {
				continue
			}
			out = append(out, s)
		}
	}
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

	s := parseSession(path)
	sessCacheMu.Lock()
	sessCache[path] = sessCacheEntry{mtime: info.ModTime(), size: info.Size(), sess: s}
	sessCacheMu.Unlock()
	return s
}

func parseSession(path string) *session {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := &session{ID: trimExt(filepath.Base(path))}
	var firstUser string // first human-authored prompt — used as the title
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
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
			if e.Type == "user" && firstUser == "" {
				if txt := meaningfulUserText(e.Message); txt != "" {
					firstUser = txt
				}
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
	if s.LastTS.IsZero() {
		return nil
	}
	// Title is always the first human-authored prompt, cropped to one line.
	if firstUser != "" {
		s.Title = titleFromText(firstUser)
	}
	return s
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
