// 이벤트 기반 변경 감지.
// fsnotify(커널 파일 이벤트)가 PROJECTS_DIR를 감시하고,
// 변경 이벤트를 SSE로 구독 중인 브라우저들에게 밀어준다.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

var (
	clientsMu sync.Mutex
	clients   = map[chan string]struct{}{}
)

func subscribe() chan string {
	ch := make(chan string, 16)
	clientsMu.Lock()
	clients[ch] = struct{}{}
	clientsMu.Unlock()
	return ch
}

func unsubscribe(ch chan string) {
	clientsMu.Lock()
	delete(clients, ch)
	clientsMu.Unlock()
}

func broadcast(kind, project, file string) {
	msg, _ := json.Marshal(map[string]string{"kind": kind, "project": project, "file": file})
	clientsMu.Lock()
	for ch := range clients {
		select { // 느린 구독자 때문에 감시 고루틴이 막히지 않게 한다
		case ch <- string(msg):
		default:
		}
	}
	clientsMu.Unlock()
}

func startWatcher() error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	// fsnotify는 비재귀라 프로젝트 루트 + 각 프로젝트 디렉터리를 개별 등록한다
	if err := w.Add(projectsDir); err != nil {
		return err
	}
	entries, _ := os.ReadDir(projectsDir)
	for _, e := range entries {
		if e.IsDir() {
			w.Add(filepath.Join(projectsDir, e.Name()))
		}
	}
	go func() {
		for ev := range w.Events {
			// 새 프로젝트 디렉터리가 생기면 감시 대상에 추가
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					w.Add(ev.Name)
					continue
				}
			}
			if !strings.HasSuffix(ev.Name, ".jsonl") {
				continue
			}
			rel, err := filepath.Rel(projectsDir, ev.Name)
			if err != nil {
				continue
			}
			parts := strings.Split(rel, string(os.PathSeparator))
			if len(parts) != 2 { // <프로젝트>/<세션>.jsonl 형태만
				continue
			}
			var kind string
			switch {
			case ev.Op&fsnotify.Create != 0:
				kind = "created"
			case ev.Op&fsnotify.Write != 0:
				kind = "modified"
			case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
				kind = "deleted"
			}
			if kind != "" {
				broadcast(kind, parts[0], parts[1])
			}
		}
	}()
	return nil
}

func sseHandler(w http.ResponseWriter, r *http.Request) {
	// Wails(macOS) WebView 의 writer 는 http.Flusher 를 구현하지 않지만 Write 가 곧바로
	// WebView 로 전달되므로, Flush 는 있으면 쓰고 없으면 생략한다.
	// (브라우저 net/http 경로에서는 Flusher 가 있어 기존과 동일하게 동작한다.)
	flush := func() {}
	if fl, ok := w.(http.Flusher); ok {
		flush = fl.Flush
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	ch := subscribe()
	defer unsubscribe(ch)
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		var err error
		select {
		case msg := <-ch:
			_, err = fmt.Fprintf(w, "data: %s\n\n", msg)
		case <-ping.C:
			_, err = fmt.Fprint(w, ": ping\n\n") // keepalive 겸 끊긴 연결 감지
		case <-r.Context().Done():
			return // 브라우저가 떠남 (net/http 경로)
		}
		if err != nil {
			// 클라이언트 연결 종료. Wails(darwin) 경로는 request context 취소가 없어
			// Write 에러가 유일한 종료 신호다 — 여기서 빠져나가 구독 채널을 정리한다.
			return
		}
		flush()
	}
}
