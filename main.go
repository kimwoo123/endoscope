// endoscope — Claude 세션 통합 로컬 대시보드.
//
// 한 바이너리·한 포트에서 두 화면을 서빙한다:
//   /        FleetBoard — 워크트리별 Claude 세션 + Git + Azure DevOps 상태 (5초 폴링)
//   /viewer  JSONL 뷰어 — 세션 하나의 대화 내용 (fsnotify + SSE 실시간)
//
// 보드의 세션 행을 클릭하면 /viewer?project=&file= 로 그 세션의 대화가 열린다.
//
// 빌드:  go build -o endoscope .
// 실행:  ./endoscope   (브라우저가 보드를 자동으로 연다)
//
// 파일 구성:
//   main.go     — 진입점: 설정 로드, 라우팅, 서버 기동
//   config.go   — 환경변수 설정 (CLAUDE_HOME, 포트, ADO_*)
//   board.go    — 보드 집계: repo→worktree→session, 상태 판정
//   sessions.go — 보드용 세션 distilled 스캔(+mtime 캐시)
//   git.go      — 워크트리 / git status 파싱
//   ado.go      — Azure DevOps REST 클라이언트
//   parse.go    — 뷰어용 JSONL 파싱: 제목 추출, 메시지 블록 펼치기
//   cache.go    — 뷰어용 증분 라인 캐시
//   api.go      — 뷰어 API 응답 조립
//   labels.go   — 사용자 라벨 저장소
//   markdown.go — Claude 텍스트 마크다운 → HTML
//   watch.go    — fsnotify 감시 + SSE 허브
//   web/        — 프론트엔드 (go:embed로 바이너리에 내장)
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"
)

//go:embed web/app.html
var appHTML []byte

// 뷰어 쪽 핸들러(api.go·labels.go·watch.go)가 참조하는 전역.
// main()에서 cfg.ClaudeHome 기준으로 채운다.
var (
	projectsDir string
	labelsFile  string
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func main() {
	cfg := loadConfig()
	projectsDir = filepath.Join(cfg.ClaudeHome, "projects")
	labelsFile = filepath.Join(cfg.ClaudeHome, "jsonl_viewer_labels.json")
	titleCacheFile = filepath.Join(cfg.ClaudeHome, "endoscope_title_cache.json")
	loadTitleCache() // 세션 제목(불변) 디스크 캐시 로드 → 재시작 후에도 머리 재파싱 회피

	var ado *adoClient
	if cfg.adoConfigured() {
		ado = newADO(cfg)
		go ado.refreshLoop() // ADO 조회를 요청 경로 밖에서 병렬로 갱신 (첫 로드 비차단)
	}

	if err := startWatcher(); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()

	// ── SPA 셸 (FleetBoard + 뷰어를 해시 라우팅으로) ──
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(appHTML)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, build(cfg, ado))
	})
	// 보드의 ⧉ VSCode 뱃지가 호출. `code <경로>`로 그 워크트리를 연다 — 이미 열린
	// 창이면 VSCode가 그 창으로 포커스하므로 워크스페이스 "설정 저장?" 프롬프트가 안 뜬다.
	// 경로는 현재 보드에 실제로 있는 워크트리만 허용(임의 경로 실행 방지).
	mux.HandleFunc("/api/open", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Query().Get("path")
		if !isBoardWorktree(cfg, ado, path) {
			http.Error(w, "unknown worktree", http.StatusBadRequest)
			return
		}
		if err := exec.Command("code", path).Start(); err != nil {
			http.Error(w, "code 실행 실패: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})

	// ── 구 /viewer URL 호환 → 해시 라우트로 리다이렉트 (해시는 서버로 안 오므로 직접 구성) ──
	mux.HandleFunc("/viewer", func(w http.ResponseWriter, r *http.Request) {
		target := "/#/viewer"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listProjects())
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listSessions(r.URL.Query().Get("project")))
	})
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, renderSession(q.Get("project"), q.Get("file")))
	})
	mux.HandleFunc("/api/label", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct{ Project, File, Label string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": setLabel(body.Project, body.File, body.Label)})
	})
	mux.HandleFunc("/api/events", sseHandler)

	addr := "127.0.0.1:" + cfg.Port
	url := "http://" + addr
	fmt.Printf("endoscope → %s\n", url)
	fmt.Printf("  CLAUDE_HOME = %s\n", cfg.ClaudeHome)
	if cfg.adoConfigured() {
		fmt.Printf("  Azure DevOps = PAT 설정됨 (org/project/repo는 각 repo 리모트에서 자동 판별)\n")
	} else {
		fmt.Printf("  Azure DevOps = (미설정; ADO_PAT 를 설정하세요)\n")
	}
	fmt.Printf("  뷰어 = %s/viewer  (종료: Ctrl+C)\n", url)

	go func() {
		time.Sleep(800 * time.Millisecond)
		exec.Command("open", url).Start()
	}()
	log.Fatal(http.ListenAndServe(addr, mux))
}
