// narcissus — Claude 세션 통합 로컬 대시보드.
//
// 한 바이너리·한 포트에서 두 화면을 서빙한다:
//   /        FleetBoard — 워크트리별 Claude 세션 + Git + Azure DevOps 상태 (5초 폴링)
//   /viewer  JSONL 뷰어 — 세션 하나의 대화 내용 (fsnotify + SSE 실시간)
//
// 보드의 세션 행을 클릭하면 /viewer?project=&file= 로 그 세션의 대화가 열린다.
//
// 빌드:  go build -o narcissus .
// 실행:  ./narcissus   (브라우저가 보드를 자동으로 연다)
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

//go:embed web/board.html
var boardHTML []byte

//go:embed web/viewer.html
var viewerHTML []byte

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

	var ado *adoClient
	if cfg.adoConfigured() {
		ado = newADO(cfg)
	}

	if err := startWatcher(); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()

	// ── 보드 (FleetBoard) ──
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(boardHTML)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, build(cfg, ado))
	})

	// ── 뷰어 (JSONL viewer) ──
	mux.HandleFunc("/viewer", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(viewerHTML)
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
	fmt.Printf("narcissus → %s\n", url)
	fmt.Printf("  CLAUDE_HOME = %s\n", cfg.ClaudeHome)
	if cfg.adoConfigured() {
		fmt.Printf("  Azure DevOps = %s/%s\n", cfg.ADOOrg, cfg.ADOProject)
	} else {
		fmt.Printf("  Azure DevOps = (미설정; ADO_ORG / ADO_PROJECT / ADO_PAT)\n")
	}
	fmt.Printf("  뷰어 = %s/viewer  (종료: Ctrl+C)\n", url)

	go func() {
		time.Sleep(800 * time.Millisecond)
		exec.Command("open", url).Start()
	}()
	log.Fatal(http.ListenAndServe(addr, mux))
}
