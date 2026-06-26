// 사용자 라벨 저장소.
// ai-title은 Claude Code 소유라 건드리지 않고,
// 내가 붙인 라벨은 뷰어 전용 별도 파일에 저장한다. 라벨이 있으면 우선 표시.
package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

var labelsMu sync.Mutex

func loadLabels() map[string]string {
	data, err := os.ReadFile(labelsFile)
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil || m == nil {
		return map[string]string{}
	}
	return m
}

func labelPtr(labels map[string]string, key string) *string {
	if l, ok := labels[key]; ok {
		return &l
	}
	return nil
}

func setLabel(project, file, label string) bool {
	if _, ok := sessionPath(project, file); !ok {
		return false
	}
	label = strings.TrimSpace(label)
	labelsMu.Lock()
	defer labelsMu.Unlock()
	m := loadLabels()
	key := project + "/" + file
	if label == "" {
		delete(m, key) // 빈 라벨 → 제거 (ai-title 표시로 복귀)
	} else {
		m[key] = label
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(labelsFile, b, 0o644) == nil
}
