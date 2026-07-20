# endoscope 관리 Makefile — 브라우저 버전과 Wails 데스크톱 앱을 한 곳에서 관리한다.
# run/app 은 각 런처 스크립트(.env 로딩 포함)를 그대로 호출한다.
#   브라우저:  make run      (run.sh)
#   데스크톱:  make app      (build-app.sh)

WAILS := $(shell command -v wails 2>/dev/null || echo $(shell go env GOPATH)/bin/wails)

.DEFAULT_GOAL := help
.PHONY: help run build app app-build dev test clean doctor

help: ## 타깃 목록 표시
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-11s\033[0m %s\n", $$1, $$2}'

run: ## 브라우저 버전 빌드+실행 (보드 자동 오픈)
	./run.sh

build: ## 브라우저 버전 바이너리만 빌드 (./endoscope)
	go build -o endoscope .

app: ## Wails 데스크톱 앱 빌드+실행
	./build-app.sh

app-build: ## Wails 데스크톱 앱 빌드만 (실행 안 함)
	./build-app.sh --no-run

dev: ## Wails 개발 모드 (핫 리로드)
	$(WAILS) dev -tags wails

test: ## go 테스트 실행
	go test ./...

clean: ## 빌드 산출물 제거 (endoscope, build/bin)
	rm -f endoscope
	rm -rf build/bin

doctor: ## Wails 환경 점검
	$(WAILS) doctor
