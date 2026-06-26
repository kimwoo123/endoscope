// JSONL 파싱: 세션 파일을 라인 단위로 읽고,
// 메시지 content를 화면용 블록(text/thinking/tool/result)으로 펼친다.
package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 본문에서 걸러낼 자동 삽입 래퍼 태그 (사용자가 직접 친 글이 아님).
// Go 정규식(RE2)은 역참조(\1)를 지원하지 않아 태그별로 풀어서 쓴다.
var wrapperRe = regexp.MustCompile(`(?s)<ide_opened_file>.*?</ide_opened_file>` +
	`|<ide_selection>.*?</ide_selection>` +
	`|<system-reminder>.*?</system-reminder>` +
	`|<command-[a-z-]+>.*?</command-[a-z-]+>`)

func stripWrappers(s string) string {
	return strings.TrimSpace(wrapperRe.ReplaceAllString(s, ""))
}

func str(v any) string { s, _ := v.(string); return s }

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …"
}

func msgContent(obj map[string]any) any {
	msg, _ := obj["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	return msg["content"]
}

func firstUserText(obj map[string]any) string {
	switch c := msgContent(obj).(type) {
	case string:
		return stripWrappers(c)
	case []any:
		for _, b := range c {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if bm["type"] == "text" {
				if t := stripWrappers(str(bm["text"])); t != "" {
					return t
				}
			}
		}
	}
	return ""
}

// sessionID는 라인 내용에 박힌 실제 sessionId를 쓰고, 없으면 파일명(fallback)으로 둔다.
func sessionID(lines []map[string]any) string {
	for _, o := range lines {
		if id := str(o["sessionId"]); id != "" {
			return id
		}
	}
	return ""
}

func sessionTitle(lines []map[string]any) string {
	// 제목은 항상 첫 사용자 메시지를 기준으로 한다 (보드와 동일한 기준).
	// titleFromText로 줄여 보드/뷰어가 같은 형태의 제목을 보여준다.
	for _, o := range lines {
		if o["type"] == "user" {
			if t := firstUserText(o); t != "" {
				return titleFromText(t)
			}
		}
	}
	// 사용자 텍스트가 전혀 없을 때만 Claude가 붙인 ai-title로 폴백.
	// (ai-title은 갱신될 때마다 라인이 추가되므로 마지막 것이 최신)
	title := ""
	for _, o := range lines {
		if o["type"] == "ai-title" {
			if t := str(o["aiTitle"]); t != "" {
				title = t
			}
		}
	}
	if title != "" {
		return title
	}
	return "(제목 없음)"
}

func formatToolUse(name string, input map[string]any) string {
	head := "🔧 " + name
	if len(input) == 0 {
		return head
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys) // Go map은 순서가 없으므로 키를 정렬해 표시를 안정화
	parts := []string{head}
	for _, k := range keys {
		var val string
		if s, ok := input[k].(string); ok {
			val = s
		} else {
			b, _ := json.MarshalIndent(input[k], "", "  ")
			val = string(b)
		}
		val = truncRunes(val, 800)
		// 여러 줄 값은 들여쓰기해서 가독성 유지
		if strings.Contains(val, "\n") {
			ls := strings.Split(val, "\n")
			for i := range ls {
				ls[i] = "    " + ls[i]
			}
			val = "\n" + strings.Join(ls, "\n")
		}
		parts = append(parts, "  "+k+": "+val)
	}
	return strings.Join(parts, "\n")
}

func resultText(b map[string]any) string {
	var c string
	switch v := b["content"].(type) {
	case string:
		c = v
	case []any:
		var sb strings.Builder
		for _, x := range v {
			if xm, ok := x.(map[string]any); ok {
				sb.WriteString(str(xm["text"]))
			}
		}
		c = sb.String()
	case nil:
		c = ""
	default:
		c = fmt.Sprint(v)
	}
	return truncRunes(c, 1000)
}

type block struct {
	Kind   string  `json:"kind"`
	Text   string  `json:"text"`
	HTML   string  `json:"html,omitempty"`   // 마크다운 렌더링 결과 (Claude 텍스트만)
	Result *string `json:"result,omitempty"`
	ref    string  // tool_use는 자기 id, tool_result는 대응하는 tool_use_id
}

func iterBlocks(obj map[string]any) []block {
	var out []block
	switch c := msgContent(obj).(type) {
	case string:
		out = append(out, block{Kind: "text", Text: c})
	case []any:
		for _, b := range c {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			switch bm["type"] {
			case "text":
				out = append(out, block{Kind: "text", Text: str(bm["text"])})
			case "thinking":
				t := str(bm["thinking"])
				if t == "" {
					t = str(bm["text"])
				}
				out = append(out, block{Kind: "thinking", Text: t})
			case "tool_use":
				name := str(bm["name"])
				if name == "" {
					name = "?"
				}
				input, _ := bm["input"].(map[string]any)
				out = append(out, block{Kind: "tool", Text: formatToolUse(name, input), ref: str(bm["id"])})
			case "tool_result":
				out = append(out, block{Kind: "result", Text: resultText(bm), ref: str(bm["tool_use_id"])})
			}
		}
	}
	return out
}

func fmtTime(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04")
}
