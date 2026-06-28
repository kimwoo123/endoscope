#!/usr/bin/env bash
# endoscope 런처 — 보드(/)와 뷰어(/viewer)를 한 바이너리·한 포트에서 띄운다.
# ADO_*, FLEETBOARD_* 등 환경변수는 그대로 상속된다. 종료는 Ctrl+C.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# .env (gitignore됨)에 ADO_*, FLEETBOARD_* 를 적어두면 자동 로드된다.
if [ -f "$ROOT/.env" ]; then
  echo "▶ loading .env"
  set -a; . "$ROOT/.env"; set +a
fi

echo "▶ building…"
( cd "$ROOT" && go build -o endoscope . )

echo "▶ endoscope → http://127.0.0.1:${FLEETBOARD_PORT:-7777}  (보드, 브라우저 자동 오픈)"
echo "▶ 뷰어     → http://127.0.0.1:${FLEETBOARD_PORT:-7777}/viewer"
exec "$ROOT/endoscope"
