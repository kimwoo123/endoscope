#!/usr/bin/env bash
# endoscope 데스크톱 앱 빌드·실행 — Wails 네이티브 창 버전.
# (브라우저 버전은 run.sh 를 쓴다.)
#
# 본인 전용이라 코드 서명·공증은 하지 않는다. 로컬에서 직접 빌드한 .app 은
# quarantine 속성이 없어 Gatekeeper 경고 없이 그대로 실행된다.
#
# 사용:  ./build-app.sh          # 빌드 후 앱 실행
#        ./build-app.sh --no-run # 빌드만
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# .env (gitignore됨)에 ADO_*, FLEETBOARD_* 를 적어두면 자동 로드된다.
if [ -f "$ROOT/.env" ]; then
  echo "▶ loading .env"
  set -a; . "$ROOT/.env"; set +a
fi

# wails CLI 경로 (PATH 에 없으면 GOPATH/bin 에서 찾는다)
WAILS="$(command -v wails || echo "$(go env GOPATH)/bin/wails")"
if [ ! -x "$WAILS" ]; then
  echo "✗ wails CLI 를 찾을 수 없습니다. 설치: go install github.com/wailsapp/wails/v2/cmd/wails@latest" >&2
  exit 1
fi

echo "▶ building endoscope.app (wails, -tags wails)…"
( cd "$ROOT" && "$WAILS" build -tags wails )

APP="$ROOT/build/bin/endoscope.app"
echo "▶ built → $APP"

if [ "${1:-}" != "--no-run" ]; then
  echo "▶ launching…"
  open "$APP"
fi
