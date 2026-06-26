// 마크다운 렌더링.
// Claude 텍스트 블록은 70%가 마크다운이라 서버에서 HTML로 변환한다.
//
// 보안: goldmark 기본 설정은 본문 속 원시 HTML(<script> 등)을 이스케이프한다
// (html.WithUnsafe()를 켜지 않음). 따라서 변환 결과를 그대로 innerHTML 해도 안전하다.
package main

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var md = goldmark.New(goldmark.WithExtensions(extension.GFM)) // 표·취소선·자동링크 등

// renderMarkdown은 변환된 HTML을 돌려준다. 실패하면 "" (호출부가 원문으로 폴백).
func renderMarkdown(s string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(s), &buf); err != nil {
		return ""
	}
	return buf.String()
}
