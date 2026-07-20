// server.go — 브라우저 진입점(main.go)과 데스크톱 진입점(main_wails.go)이
// 공유하는 초기화·라우팅. setup()이 완성된 http.Handler 를 돌려준다.
package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
)

//go:embed web/app.html
var appHTML []byte

// 뷰어 쪽 핸들러(api.go·labels.go·watch.go)가 참조하는 전역.
// setup()에서 cfg.ClaudeHome 기준으로 채운다.
var (
	projectsDir string
	labelsFile  string
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// setup 은 설정을 로드하고 전역/워처/ADO 를 초기화한 뒤 라우팅된 핸들러를 돌려준다.
// 서버 기동(ListenAndServe)이나 창 생성은 호출자(각 진입점)가 맡는다.
func setup() (http.Handler, Config) {
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

	// ── SPA 셸 (Board + Viewer 를 해시 라우팅으로) ──
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

	return mux, cfg
}
