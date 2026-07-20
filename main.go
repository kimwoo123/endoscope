//go:build !wails

// endoscope — Claude 세션 통합 로컬 대시보드 (브라우저 진입점).
//
// 한 바이너리·한 포트에서 세 화면을 서빙한다:
//   #/board   Board  — 워크트리별 Claude 세션 + Git + Azure DevOps 상태 (5초 폴링)
//   #/viewer  Viewer — 세션 하나의 대화 내용 (fsnotify + SSE 실시간)
//   #/code    Code   — 워크트리 파일 트리 + 코드 열람 (문법 하이라이팅)
//
// 보드의 세션 행을 클릭하면 /viewer?project=&file= 로 그 세션의 대화가 열린다.
//
// 빌드:  go build -o endoscope .       (브라우저 버전, 기본)
//        wails build -tags wails       (네이티브 데스크톱 앱 — main_wails.go)
// 실행:  ./endoscope   (브라우저가 보드를 자동으로 연다)
//
// 파일 구성:
//   main.go       — 브라우저 진입점: 서버 기동 + 브라우저 자동 열기
//   main_wails.go — 데스크톱 진입점: Wails 네이티브 창 (build tag: wails)
//   server.go     — 두 진입점 공용 setup(): 설정 로드, 라우팅
//   config.go     — 환경변수 설정 (CLAUDE_HOME, 포트, ADO_*)
//   board.go      — 보드 집계: repo→worktree→session, 상태 판정
//   sessions.go   — 보드용 세션 distilled 스캔(+mtime 캐시)
//   git.go        — 워크트리 / git status 파싱
//   ado.go        — Azure DevOps REST 클라이언트
//   parse.go      — 뷰어용 JSONL 파싱: 제목 추출, 메시지 블록 펼치기
//   cache.go      — 뷰어용 증분 라인 캐시
//   api.go        — 뷰어 API 응답 조립
//   code.go       — 코드 뷰어: 워크트리 파일 트리 + chroma 하이라이팅
//   labels.go     — 사용자 라벨 저장소
//   markdown.go   — Claude 텍스트 마크다운 → HTML
//   watch.go      — fsnotify 감시 + SSE 허브
//   web/          — 프론트엔드 (go:embed로 바이너리에 내장)
package main

import (
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"time"
)

func main() {
	handler, cfg := setup()

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
	log.Fatal(http.ListenAndServe(addr, handler))
}
