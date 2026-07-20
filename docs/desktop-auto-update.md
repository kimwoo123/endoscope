# endoscope 데스크톱 앱 — 자동 업데이트 설계

> 상태: **보류(설계만 유지)**. 배포 범위를 **본인 전용**으로 결정 → 자동 업데이트는 구현하지 않는다.
> 본인 전용에선 로컬 `./build-app.sh` (= `wails build`) 로 직접 최신 빌드를 얻는 게 더 단순하고,
> 로컬 빌드 `.app` 은 quarantine 이 없어 서명·공증 없이 실행된다. 이 문서는 **나중에 타인에게
> 배포하게 될 경우**를 위해 그대로 보존한다. 대상: macOS(arm64), Wails v2.13 기반 `main_wails.go`.

## 1. 배경 · 제약

- **Wails v2 에는 내장 자동 업데이터가 없다.** 업데이트는 직접 붙여야 한다.
- **진짜 비용은 업데이트 메커니즘이 아니라 macOS 코드 서명·공증(notarization)이다.**
  - App Store 밖에서 `.app` 을 배포/실행하려면 **Developer ID Application 인증서로 서명 + Apple 공증 + staple** 이 되어 있어야 Gatekeeper 를 통과한다. 아니면 "손상되었거나 개발자를 확인할 수 없음" 으로 실행이 막힌다.
  - **Apple Developer Program 가입($99/년)** 필요.
  - 자동 업데이트로 새 빌드를 내려줘도, 그 빌드가 서명·공증되어 있지 않으면 똑같이 막힌다. 즉 **서명·공증 파이프라인이 자동 업데이트의 선행 조건**이다.
- 현재 원격: `github.com/kimwoo123/endoscope` → **GitHub Releases** 를 업데이트 채널로 그대로 쓸 수 있다.

### 먼저 정해야 할 것: 배포 범위

| 범위 | 서명/공증 필요성 |
|------|------------------|
| **본인만 사용** | 공증 생략 가능. 로컬에서 `xattr -dr com.apple.quarantine endoscope.app` 로 격리 해제하면 실행됨. 단 새 빌드마다 매번 해제 필요 → "자동 업데이트"의 매끄러움은 떨어짐 |
| **팀/타인 배포** | Developer ID 서명 + 공증 **필수**. 이게 없으면 자동 업데이트 자체가 의미 없음 |

→ 이 선택이 이후 모든 단계의 무게를 결정한다.

## 2. 업데이트 메커니즘 비교

| 방식 | 장점 | 단점 | 이 앱 적합도 |
|------|------|------|--------------|
| **A. UI 알림 + GitHub Releases** (notify-only) | 구현 최소, CGo 불필요, 이미 있는 웹 UI 재사용. 서명 복잡도 없음(사용자가 새 버전 링크로 받음) | "원클릭"이 아님 — 사용자가 직접 내려받아 교체 | ★★★ MVP 로 최적 |
| **B. minio/selfupdate** (순수 Go) | 크로스플랫폼, 실행 바이너리 교체 | `.app` 번들은 안쪽 Mach-O만 바꾸면 서명 깨짐 → 번들 통째 교체 + 재서명 로직 직접 작성 필요 | ★★ CLI엔 좋지만 .app엔 손이 감 |
| **C. Sparkle** (macOS 표준) | 백그라운드 무음 업데이트, 델타, EdDSA 서명 검증, 성숙한 UX | Sparkle.framework 번들 + Info.plist(SUFeedURL/SUPublicEDKey) + CGo(Obj-C) 브리지 필요. Windows는 WinSparkle 별도 | ★★ 장기 정답, 초기 비용 큼 |
| **D. 수동** | 없음 | 자동 아님 | — |

## 3. 권장안

**2단계 전략:**

- **MVP = 방식 A (UI 알림 + GitHub Releases).**
  이 앱은 이미 HTTP 백엔드 + 웹 UI 를 갖고 있어, 업데이트 확인을 백엔드에 붙이고 UI 배지로 알리는 게 가장 저렴하고 견고하다. CGo·프레임워크 번들 불필요.
- **장기(원한다면) = 방식 C(Sparkle) 로 승격.**
  무음 원클릭 업데이트가 필요해지면 그때 Sparkle 도입. 단 공증된 appcast + CGo 브리지 예산 확보.

두 경우 모두 **선행: Developer ID 서명 + 공증 파이프라인**(배포 범위가 '타인'일 때).

## 4. MVP 구현 스케치 (방식 A)

### 4.1 버전 임베드
```
wails build -tags wails -ldflags "-X main.version=v1.2.3"
```
```go
// main_wails.go (또는 공용 파일)
var version = "dev" // 빌드 시 ldflags 로 주입
```

### 4.2 백엔드: 업데이트 확인 엔드포인트
`server.go` 의 mux 에 추가 (약 1시간 캐시, 요청 경로 밖에서 갱신):
```go
// GET /api/update → {current, latest, url, updateAvailable}
// https://api.github.com/repos/kimwoo123/endoscope/releases/latest 의
// tag_name 을 embedded version 과 비교(semver).
```
- ADO 클라이언트(`ado.go`)의 `refreshLoop` 패턴을 그대로 재사용해 비차단 캐시.
- GitHub API 무인증 60req/시간 제한 → 시간당 1회면 충분.

### 4.3 프론트엔드: 헤더 배지
- `app.html` 로드 시 `/api/update` 폴링(또는 기존 5초 상태 폴링에 합치기).
- `updateAvailable` 이면 상단 헤더(현재 ADO 상태칩 자리 옆)에 "새 버전 vX.Y ↗" 배지 표시 → 클릭 시 릴리스 URL 열기.
- 데스크톱에서 외부 링크 열기는 Wails `runtime.BrowserOpenURL` 또는 기존 `/api/open` 패턴 응용.

### 4.4 메뉴 항목
- "Check for Updates…" 메뉴 아이템 추가(현재 App/Edit 메뉴에 커스텀 항목 append) → 같은 확인 로직 트리거, 결과 다이얼로그.

## 5. 서명 · 공증 파이프라인 (선행, 배포 시)

```
# 1) 서명 (Developer ID Application)
codesign --deep --force --options runtime \
  --sign "Developer ID Application: <NAME> (<TEAMID>)" \
  build/bin/endoscope.app

# 2) 공증 (notarytool) — zip 으로 제출
ditto -c -k --keepParent build/bin/endoscope.app endoscope.zip
xcrun notarytool submit endoscope.zip \
  --apple-id <APPLE_ID> --team-id <TEAMID> --password <APP_SPECIFIC_PW> --wait

# 3) staple
xcrun stapler staple build/bin/endoscope.app
```
- Wails 는 서명·공증을 CI(GitHub Actions)에서 자동화하는 가이드를 제공한다.
- 릴리스: staple 된 `.app` 을 zip 으로 GitHub Releases 에 업로드 → 방식 A 가 이 tag/asset 을 가리킴.

## 6. 단계별 계획

1. **결정**: 배포 범위(본인 전용 / 타인 배포) → 서명·공증 필요 여부 확정.
2. (타인 배포 시) **서명·공증 스크립트/CI 구성** (§5). 본인 전용이면 quarantine 해제 문서화로 대체.
3. **버전 임베드** (ldflags) + 빌드 스크립트에 반영 (§4.1).
4. **MVP 업데이트 확인** (§4.2~4.4): 백엔드 엔드포인트 + 헤더 배지 + 메뉴 항목.
5. **(선택) Sparkle 승격**: 무음 원클릭이 필요해지면.

## 7. 열린 질문

- 배포 범위(본인 전용 vs 타인)? — §1 표, 전체 무게를 결정.
- 릴리스 채널을 GitHub Releases 로 확정할지.
- Windows/Linux 데스크톱도 대상인지(그렇다면 방식 A 는 그대로, Sparkle 은 WinSparkle 등 별도).
