// 파싱한 라인의 증분 캐시.
//
// JSONL 세션은 거의 append-only다. 그래서 파일이 커지면 '추가된 꼬리'만
// 파싱하고, 변경이 없으면 캐시를 그대로 돌려준다.
//   - 활성 세션 갱신(SSE modified): 매번 전체 재파싱 → 새 바이트만 파싱
//   - 목록 조회(listSessions): 모든 세션 재파싱 → 바뀐 세션만 재파싱
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
)

type cachedLines struct {
	rawSize  int64            // 마지막으로 본 파일 크기 (변경 감지용)
	consumed int64            // 개행으로 끝난 완전한 라인까지 파싱한 오프셋
	lines    []map[string]any // 파싱 결과 (읽기 전용으로 공유)
}

var (
	lineCacheMu sync.Mutex
	lineCache   = map[string]*cachedLines{}
)

// 완전한 라인(개행으로 끝나는)만 파싱하고 소비한 바이트 수를 함께 돌려준다.
// 끝에 개행 없는 부분 라인은 다음 호출로 미룬다 (쓰는 도중의 반쪽 JSON 방지).
func parseLines(data []byte) ([]map[string]any, int) {
	var out []map[string]any
	consumed := 0
	for {
		nl := bytes.IndexByte(data[consumed:], '\n')
		if nl < 0 {
			break
		}
		line := bytes.TrimSpace(data[consumed : consumed+nl])
		consumed += nl + 1
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if json.Unmarshal(line, &obj) == nil {
			out = append(out, obj)
		}
	}
	return out, consumed
}

// loadSession은 캐시를 활용해 파일을 파싱한다.
// 변경 없으면 캐시 반환, 커졌으면 꼬리만 파싱해 이어 붙인다.
func loadSession(path string) []map[string]any {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	size := fi.Size()

	lineCacheMu.Lock()
	c := lineCache[path]
	lineCacheMu.Unlock()

	if c != nil && c.rawSize == size {
		return c.lines // 변경 없음 → 재파싱 안 함
	}

	f, err := os.Open(path)
	if err != nil {
		if c != nil {
			return c.lines
		}
		return nil
	}
	defer f.Close()

	var base []map[string]any
	var start int64
	if c != nil && size > c.rawSize {
		base = c.lines    // 파일이 커짐 → 추가된 꼬리만 읽는다
		start = c.consumed
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			base, start = nil, 0 // 시크 실패 시 전체 재파싱으로 폴백
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		if c != nil {
			return c.lines
		}
		return nil
	}
	newLines, n := parseLines(data)

	// 기존 readers의 backing array를 건드리지 않도록 새 슬라이스에 복사
	combined := make([]map[string]any, 0, len(base)+len(newLines))
	combined = append(combined, base...)
	combined = append(combined, newLines...)

	lineCacheMu.Lock()
	lineCache[path] = &cachedLines{rawSize: size, consumed: start + int64(n), lines: combined}
	lineCacheMu.Unlock()
	return combined
}
