# endoscope

여러 워크트리에서 도는 **Claude 세션 + Git 상태 + Azure DevOps 파이프라인**을 한 화면에서 보고,
세션 하나를 클릭하면 그 **대화 내용**까지 읽을 수 있는 로컬 웹 대시보드입니다.
외부 런타임 없이 도는 **Go 단일 바이너리**이고, HTML/JS/CSS는 바이너리 안에 내장됩니다.

한 바이너리·한 포트에서 두 화면을 서빙합니다:

| 경로 | 화면 | 갱신 |
|---|---|---|
| `/` | **FleetBoard** — 워크트리별 세션·Git·ADO 상태 보드 | 5초 폴링 |
| `/viewer` | **JSONL 뷰어** — 세션 하나의 대화 내용 (분할뷰 최대 4) | fsnotify + SSE 실시간 |

보드의 세션 행을 클릭하면 `/viewer?project=&file=` 로 그 세션의 대화가 새 탭에서 열립니다.

```
┌ pozzetti  /Users/you/pozzetti ─────────────────────────────────────────────┐
│ feat/admin  ●2 ↑1  ● VSCode            PR !314   ● succeeded #482  ⧉ VSCode  │
│   ✋ 입력대기  Plan admin role feature   특정 유저만 admin 권한을…      12분 전  │  ← 제목 클릭 → 뷰어
│   ⏳ 작업중   Connect frontend button   백엔드 push 하자               방금     │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 무엇을 보여주나 (보드)

| 축 | 출처 | 표시 |
|---|---|---|
| **Claude** | `CLAUDE_HOME/projects/**/*.jsonl` 의 첫 사용자 메시지·`last-prompt`·타임스탬프 | 제목 / 마지막 지시 / 상태 |
| **VSCode** | `CLAUDE_HOME/ide/*.lock` 의 `workspaceFolders` | `● VSCode` (현재 열려있음) |
| **Git** | 워크트리별 `git worktree list` + `git status -sb` | `clean` / `●N`(dirty) / `↑↓`(ahead/behind) |
| **Pipeline** | Azure DevOps REST `builds` | `● succeeded/failed #번호` (클릭 → 로그) |
| **PR** | Azure DevOps REST `pullrequests` | `PR !번호` (클릭 → PR) |

### 세션 상태
- `⏳ 작업중` — 최근(기본 45초 내) 활동 = Claude가 응답 생성 중
- `✋ 입력대기` — VSCode 열려있고 마지막이 Claude 차례로 끝났으며 최근(기본 24h 내) = **당신을 기다리는 중**
- `💤 idle` — 그 외 (기본 숨김, 헤더 토글로 표시)

## 대화 뷰어 (`/viewer`)

- **세션 탐색** — 프로젝트 선택 → 세션 목록. 제목은 **첫 사용자 메시지**를 씁니다(보드와 동일 기준).
- **분할뷰** — 세로 컬럼으로 최대 4개 세션을 나란히. 클릭은 활성 패널 교체, **⌘/Ctrl+클릭**은 새 패널.
- **대화 표시** — 내 메시지는 우측 파란 말풍선, Claude는 좌측 초록 말풍선. 최신 메시지가 맨 위.
- **도구 호출 묶음** — 도구 호출과 그 결과를 한 카드로 짝지어 표시.
- **마크다운 렌더링** — Claude 답변을 HTML로 렌더링 (내 메시지·도구 로그는 원문 유지).
- **메시지별 토큰** — 어시스턴트 턴의 출력/입력/캐시 토큰 표기.
- **실시간 갱신** — 파일이 바뀌면 화면이 즉시 따라옵니다 (`tail -f`처럼 관찰).
- **사용자 라벨** — 세션에 내 이름표를 달 수 있습니다. 원본 파일은 건드리지 않습니다.

## 요구사항

- Go 1.24+
- macOS / Linux / Windows (파일 감시는 fsnotify가 OS별 커널 API로 처리)

## 실행

```bash
go build -o endoscope .
./endoscope              # → http://127.0.0.1:7777  (보드, 브라우저 자동 오픈)
```

또는 런처로:

```bash
./run.sh                 # 빌드 후 실행
```

종료는 `Ctrl+C`.

## 설정 (전부 환경변수 — 어느 로컬에서나 그대로)

| 변수 | 기본값 | 설명 |
|---|---|---|
| `FLEETBOARD_PORT` | `7777` | 포트 (보드·뷰어 공용) |
| `CLAUDE_HOME` | `~/.claude` | Claude Code 데이터 위치 |
| `FLEETBOARD_MAX_AGE_HOURS` | `168` (7일) | 이 기간 안에 활동한 세션만 보드에 표시 |
| `FLEETBOARD_WAITING_HOURS` | `24` | 이보다 오래된 세션은 `입력대기` 대신 `idle` |
| `FLEETBOARD_RUNNING_SEC` | `45` | 이보다 최근 활동이면 `작업중` |
| `FLEETBOARD_REPOS` | — | 세션이 없어도 항상 표시할 레포 경로(`,` 또는 `:` 구분) |
| `FLEETBOARD_BASE_BRANCH` | `main` | merged PR 판정의 머지 대상 브랜치 |
| `ADO_PAT` | — | Azure DevOps Personal Access Token (Build: Read, Code: Read) |

**`ADO_PAT`만 설정하면 됩니다.** org/project/repo는 각 레포의 git `origin` 리모트에서
자동 판별하므로(여러 ADO 프로젝트가 섞여 있어도 각각 올바르게 조회), 별도 org/project 설정이
필요 없습니다. ADO가 아닌 레포(GitHub 등)는 파이프라인/PR·merged 열이 자동으로 빠집니다.
PAT가 비어 있으면 ADO 관련 열만 전부 빠지고 나머지는 그대로 동작합니다.

```bash
export ADO_PAT=xxxxxxxxxxxx
./endoscope
```

> 토큰은 코드에 박지 않고 `os.Getenv("ADO_PAT")` 로만 읽습니다.

## 코드 구조

| 파일 | 역할 |
|---|---|
| `main.go` | 진입점 — 설정 로드, 보드·뷰어 라우팅, 서버 기동 |
| `config.go` | 환경변수 설정 |
| **보드** | |
| `board.go` | repo → 워크트리 → 세션 집계, 상태 판정 |
| `sessions.go` | 보드용 세션 jsonl distilled 스캔(+mtime 캐시) |
| `git.go` | 워크트리 / git status 파싱 |
| `ado.go` | Azure DevOps REST 클라이언트(+20초 캐시) |
| **뷰어** | |
| `parse.go` | JSONL 파싱 — 제목 추출, 메시지 블록(text/thinking/tool/result) 펼치기 |
| `cache.go` | 증분 파싱 캐시 — 바뀐 꼬리만 다시 파싱 |
| `api.go` | 응답 조립 — 목록/세션 렌더링, 경로 검증 |
| `markdown.go` | Claude 텍스트 마크다운 → HTML (goldmark) |
| `labels.go` | 사용자 라벨 저장소 |
| `watch.go` | 변경 감지 — fsnotify 감시 + SSE 허브 |
| `web/board.html` | 보드 UI (go:embed) |
| `web/viewer.html` | 뷰어 UI (go:embed) |

> 보드와 뷰어는 같은 `~/.claude/projects/**/*.jsonl` 를 읽되, 접근 패턴이 달라
> 파싱 경로를 둘로 둡니다 — 보드는 모든 파일을 가볍게 distilled 스캔(`sessions.go`),
> 뷰어는 한 파일의 전체 대화를 증분 캐시로 렌더(`parse.go`+`cache.go`).
> 제목은 `titleFromText`를 공유해 양쪽이 동일한 형태로 보여줍니다.

## API

| 엔드포인트 | 설명 |
|---|---|
| `GET /api/state` | 보드 상태 (repo/worktree/session/git/ado) |
| `GET /api/projects` | 뷰어: 프로젝트 목록 |
| `GET /api/sessions?project=` | 뷰어: 해당 프로젝트의 세션 목록 |
| `GET /api/session?project=&file=` | 뷰어: 세션 대화 내용 (마크다운 렌더 포함) |
| `POST /api/label` | 뷰어: 라벨 설정/제거 `{project, file, label}` |
| `GET /api/events` | 뷰어: SSE 변경 스트림 |

## 데이터 · 보안

- 서버는 `127.0.0.1`에만 바인딩됩니다 (로컬 전용).
- 뷰어의 모든 경로는 `~/.claude/projects` 안으로 제한됩니다 (디렉터리 탈출 차단).
- 세션 원본 파일은 **읽기 전용**으로만 다룹니다. 쓰기는 별도 라벨 파일에만 일어납니다.
- 마크다운은 goldmark를 `unsafe` 옵션 없이 사용 — 본문 속 원시 HTML은 렌더링되지 않습니다(XSS 차단).

## 의존성

- [fsnotify](https://github.com/fsnotify/fsnotify) — 크로스플랫폼 파일 감시
- [goldmark](https://github.com/yuin/goldmark) — 마크다운 → HTML (GFM)
