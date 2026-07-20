//go:build wails

// main_wails.go — endoscope 데스크톱 진입점 (네이티브 창).
//
// 브라우저 버전(main.go)과 동일한 setup() 핸들러를 그대로 Wails 의 AssetServer 에
// 물려, 시스템 브라우저 대신 네이티브 WebView 창에서 렌더링한다. 백엔드 코드는
// 100% 공유하며 ListenAndServe·브라우저 자동 열기는 사용하지 않는다.
//
// 빌드:  wails build -tags wails
// 개발:  wails dev -tags wails
package main

import (
	"context"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

func main() {
	handler, _ := setup()

	err := wails.Run(&options.App{
		Title:  "endoscope",
		Width:  1400,
		Height: 900,
		AssetServer: &assetserver.Options{
			Handler: handler, // 기존 http.ServeMux 를 그대로 재사용
		},
		OnStartup: func(ctx context.Context) {},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: false,
			About: &mac.AboutInfo{
				Title:   "endoscope",
				Message: "Claude 세션 통합 로컬 대시보드",
			},
		},
	})
	if err != nil {
		println("wails.Run error:", err.Error())
	}
}
