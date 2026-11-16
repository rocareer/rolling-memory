package router

// 本项目新增的路由接线（上游文件 router.go / index.go 保持逐字节一致，本项目新增点集中在本文件）：
//
//   - 插件接线（internal/plugins）：空白导入触发 init()，把已安装插件按注册表接进管理面路由
//     （register_gen.go 生成物 + wiring.go 挂载；出厂空骨架时无任何行为）；
//   - 插件管理面路由（internal/router/admin/plugin）：插件管理页的 list/get/enable/disable/uninstall 端点。
import (
	_ "github.com/ai-go-hub/ai-go-admin/internal/plugins"
	_ "github.com/ai-go-hub/ai-go-admin/internal/rmconsole"
	_ "github.com/ai-go-hub/ai-go-admin/internal/router/admin/plugin"
)
