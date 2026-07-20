// code.go — 코드 뷰어: 워크트리 파일 트리 + 파일 내용(chroma 하이라이팅).
//
// 열람 범위는 보드에 실제로 떠 있는 워크트리로 제한한다(/api/open 과 같은 기준).
// 트리는 디렉터리 단위 lazy 로딩이고, .gitignore 된 항목은 git check-ignore 로 걸러낸다.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

const maxCodeBytes = 1 << 20 // 1MB 초과 파일은 렌더링하지 않는다

type codeRoot struct {
	Path   string `json:"path"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

// 루트 목록은 build()(세션 스캔 + git 호출)가 비싸서 짧게 캐시한다.
// 보드 폴링 주기와 같은 5초면 워크트리 추가가 곧 반영된다.
var (
	rootsMu    sync.Mutex
	rootsCache []codeRoot
	rootsAt    time.Time
)

func codeRoots(cfg Config) []codeRoot {
	rootsMu.Lock()
	defer rootsMu.Unlock()
	if time.Since(rootsAt) < 5*time.Second && rootsCache != nil {
		return rootsCache
	}
	out := []codeRoot{}
	for _, repo := range build(cfg, nil).Repos { // 경로 열거만 — ADO 조회 불필요
		for _, wt := range repo.Worktrees {
			if wt.IsGit {
				out = append(out, codeRoot{wt.Path, repo.Name, wt.Branch})
			}
		}
	}
	rootsCache, rootsAt = out, time.Now()
	return out
}

func isCodeRoot(cfg Config, path string) bool {
	for _, r := range codeRoots(cfg) {
		if r.Path == path {
			return true
		}
	}
	return false
}

// safeJoin 은 rel 이 root 밖으로 나가지 않을 때만 절대 경로를 준다.
// "/" 를 붙여 Clean 하면 앞쪽 ".." 가 흡수되고, 심볼릭 링크 탈출은 EvalSymlinks 로 막는다.
func safeJoin(root, rel string) (string, bool) {
	p := filepath.Join(root, filepath.Clean("/"+rel))
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

type codeEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
}

// listCodeDir 은 dir 바로 아래 항목을 준다(.git 과 .gitignore 대상 제외).
func listCodeDir(root, dir string) []codeEntry {
	es, err := os.ReadDir(dir)
	if err != nil {
		return []codeEntry{}
	}
	names := make([]string, 0, len(es))
	isDir := map[string]bool{}
	for _, e := range es {
		if e.Name() == ".git" {
			continue
		}
		names = append(names, e.Name())
		isDir[e.Name()] = e.IsDir()
	}
	ignored := gitIgnored(root, dir, names)

	out := make([]codeEntry, 0, len(names))
	for _, n := range names {
		if ignored[n] {
			continue
		}
		out = append(out, codeEntry{n, isDir[n]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir // 디렉터리 먼저
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// gitIgnored 는 names 중 .gitignore 로 무시되는 이름들을 표시한다.
// check-ignore 는 무시 대상이 없으면 exit 1 이라 runGit 을 못 쓰고 직접 돌린다.
func gitIgnored(root, dir string, names []string) map[string]bool {
	out := map[string]bool{}
	if len(names) == 0 {
		return out
	}
	var in bytes.Buffer
	for _, n := range names {
		in.WriteString(filepath.Join(dir, n) + "\n")
	}
	cmd := exec.Command("git", "-C", root, "check-ignore", "--stdin")
	cmd.Stdin = &in
	res, err := cmd.Output()
	if err != nil && len(res) == 0 {
		return out
	}
	for _, line := range strings.Split(strings.TrimRight(string(res), "\n"), "\n") {
		if line != "" {
			out[filepath.Base(line)] = true
		}
	}
	return out
}

type codeFile struct {
	Path  string `json:"path"`
	Lang  string `json:"lang"`
	HTML  string `json:"html"`
	Lines int    `json:"lines"`
	Note  string `json:"note"` // 비어있지 않으면 본문 대신 이 사유를 보여준다
}

var codeFormatter = html.New(
	html.WithLineNumbers(true),
	html.LineNumbersInTable(true),
	html.TabWidth(4),
)

// readCode 는 파일을 읽어 하이라이팅된 HTML 로 만든다.
// 바이너리·초대형 파일은 Note 만 채워 돌려준다.
func readCode(rel, abs string) codeFile {
	cf := codeFile{Path: rel}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		cf.Note = "파일을 읽을 수 없습니다"
		return cf
	}
	if fi.Size() > maxCodeBytes {
		cf.Note = "파일이 너무 큽니다 (1MB 초과)"
		return cf
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		cf.Note = "파일을 읽을 수 없습니다"
		return cf
	}
	if bytes.IndexByte(src, 0) >= 0 {
		cf.Note = "바이너리 파일"
		return cf
	}

	lexer := lexers.Match(rel)
	if lexer == nil {
		lexer = lexers.Analyse(string(src))
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	cf.Lang = lexer.Config().Name
	cf.Lines = bytes.Count(src, []byte("\n")) + 1

	it, err := lexer.Tokenise(nil, string(src))
	if err != nil {
		cf.Note = "하이라이팅 실패"
		return cf
	}
	var buf bytes.Buffer
	if err := codeFormatter.Format(&buf, styles.Get("github-dark"), it); err != nil {
		cf.Note = "하이라이팅 실패"
		return cf
	}
	cf.HTML = buf.String()
	return cf
}
