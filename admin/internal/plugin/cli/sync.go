package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
)

// SyncOptions sync 的旗标
type SyncOptions struct {
	// DryRun 只预演（打印将增/删的注册行与三方对照，不改写生成物）
	DryRun bool
}

// Sync 以台账重放接线生成物 register_gen.go（：`register_gen.go` 与 DB 台账漂移
// （手改文件 / 直接改库）⇒ 状态说谎；`aigo plugin sync` 以台账重放生成物，`plugin list` 显式报差异）：
//
//   - 重放范围：**不给名 ⇒ 全域**（整份生成物 = 台账重放）；**给出名 ⇒ 只对账这些名**（该有的行补上、
//     不该有的行撤下；范围外的行原样保留，便于按插件对账或避开其它正在修复的行）。名既不在台账、
//     也不在注册文件里 ⇒ 直接报错（无对账对象，防拼错后静默无操作）；
//   - 意愿集（"该有行"的判定）：台账里 kind != declarative 且 status != failed 的插件（状态表：
//     failed 的注册行应为「无」；declarative 无 Go 包、写注册行 `go build` 必失败）；
//   - 三方对照（台账 / 进程内注册表 / 注册文件）与 `aigo plugin list` **同一收集管线、同一差异口径**
//     （collectListReport/driftNotes，list.go）；
//   - 加行前 fail-closed：**需要新补的**注册行其源码目录必须在（plugins/<名>/plugin.yaml），缺失即
//     拒绝整次重放并点名——写一条指向缺失包的注册行 = `go build` 必失败，不能靠重放制造破损；
//     重放后仍在的行若源码缺失只警示（那是既有破损，不阻塞其它对账）；撤行恒安全（删行不会破坏编译）；
//   - 文件缺生成标记（被手改）时警示并把整文件恢复为生成器输出；已一致 ⇒ 「无变化」，不重写；
//   - `--dry-run` 只预演；重放不改 DB 台账与进程内注册表（生效 = 重编译 + 重启，接线真相在编译产物）；
//   - 不写审计：admin_plugin_actions 的动作域是六项闭集（迁移注释：install/enable/disable/uninstall/
//     upgrade/verify），sync 属生成物对账、不是生命周期动作（实现口径，登记待设计稿同步）。
func Sync(ctx context.Context, env *Env, names []string, opts SyncOptions) error {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return err
	}
	report, err := collectListReport(ctx, env, db)
	if err != nil {
		return err
	}

	env.Printf("插件接线对账（以台账重放 %s）\n\n", RegistryFile)
	report.Render(env)

	// 重放范围（指定名去重去空；未知名 fail-closed）
	scope := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if _, ok := report.Items[name]; !ok {
			return fmt.Errorf("sync %s：该名既不在台账（admin_plugins）、也不在注册文件里——无对账对象（核对拼写，或先 `aigo plugin install`）", name)
		}
		scope = append(scope, name)
	}
	if len(scope) == 0 {
		scope = report.Names // 全域：出现过的全部名字（台账 ∪ 进程内 ∪ 注册文件）
		env.Printf("\n重放范围：全域（台账全部行）\n")
	} else {
		env.Printf("\n重放范围：指定 %d 名（%s）——范围外的注册行原样保留\n", len(scope), strings.Join(scope, ", "))
	}

	// 意愿集（覆盖全部台账行；范围外的行不参与增删判定）
	wantAll := make(map[string]struct{}, len(report.Rows))
	for i := range report.Rows {
		if report.Rows[i].Kind == "declarative" || report.Rows[i].Status == host.StatusFailed {
			// declarative 无 Go 包；failed 半装：状态表的注册行都应为「无」
			continue
		}
		wantAll[report.Rows[i].Name] = struct{}{}
	}
	registeredSet := make(map[string]struct{}, len(report.Registered))
	for _, name := range report.Registered {
		registeredSet[name] = struct{}{}
	}
	added := make([]string, 0, len(scope))
	removed := make([]string, 0, len(scope))
	for _, name := range scope {
		_, registered := registeredSet[name]
		_, wanted := wantAll[name]
		switch {
		case wanted && !registered:
			added = append(added, name)
		case registered && !wanted:
			removed = append(removed, name)
		}
	}

	env.Printf("重放意愿集（台账）：%d 条注册行\n", len(wantAll))
	if len(added) == 0 && len(removed) == 0 {
		env.Printf("范围内注册文件与台账一致：无增删\n")
	}
	for _, name := range added {
		env.Printf("  + %s（源码在 %s/）\n", name, PluginDir(name))
	}
	for _, name := range removed {
		env.Printf("  - %s — %s\n", name, syncRemoveReason(report, name))
	}

	// 加行前的 fail-closed：新补的行必须指向真实存在的包（重放不制造 `go build` 必失败的现场）
	blocked := make([]string, 0, len(added))
	for _, name := range added {
		if _, err := env.PluginSource(name); err != nil {
			blocked = append(blocked, fmt.Sprintf("%s（%s）", name, err))
		}
	}
	if len(blocked) > 0 {
		return fmt.Errorf("台账重放被拒（fail-closed）：%d 个需补注册行的插件源码目录缺失——"+
			"写一条指向缺失包的注册行会让 `go build` 必失败。先恢复源码（git checkout plugins/<名>）或清台账行"+
			"（`aigo plugin uninstall --keep-files <名>`）后再重放:\n  %s", len(blocked), strings.Join(blocked, "\n  "))
	}

	// 重放后的名单：范围外的行原样保留，范围内按增删修正
	removedSet := make(map[string]struct{}, len(removed))
	for _, name := range removed {
		removedSet[name] = struct{}{}
	}
	nextNames := make([]string, 0, len(report.Registered)+len(added))
	for _, name := range report.Registered {
		if _, drop := removedSet[name]; !drop {
			nextNames = append(nextNames, name)
		}
	}
	nextNames = append(nextNames, added...)

	var brokenLines []string
	for _, name := range nextNames {
		if _, err := env.PluginSource(name); err != nil {
			brokenLines = append(brokenLines, name)
		}
	}
	if len(brokenLines) > 0 {
		env.Printf("⚠ 重放后仍在的注册行引用的源码目录缺失（既有破损；恢复源码或按台账撤行）: %s\n", strings.Join(brokenLines, ", "))
	}

	target := env.Abs(RegistryFile)
	current, readErr := os.ReadFile(target)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return fmt.Errorf("读取 %s 失败: %w", RegistryFile, readErr)
	}
	next := BuildRegistryFile(nextNames)
	if bytes.Equal(current, next) {
		env.Printf("\n✓ 生成物与台账已一致（%d 条注册行；无变化，未写文件）\n", len(nextNames))
		return nil
	}
	if len(current) > 0 && !bytes.HasPrefix(current, []byte(GeneratedMarker)) {
		env.Printf("\n⚠ %s 缺生成标记（%s）：重放会把整文件恢复为生成器输出\n", RegistryFile, GeneratedMarker)
	}
	env.Printf("\n== %s 重放差异 ==\n%s", RegistryFile, RegistryDiff(current, next))
	if opts.DryRun {
		env.Printf("\n（--dry-run：以上为预演，未改写 %s；去掉 --dry-run 执行重放）\n", RegistryFile)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建 %s 的父目录失败: %w", RegistryFile, err)
	}
	if err := atomicWriteFile(target, next, 0o644); err != nil {
		return fmt.Errorf("改写 %s 失败: %w", RegistryFile, err)
	}
	env.Printf("\n✓ 已按台账重放 %s：注册行 %d → %d（新增 %d / 撤下 %d）\n",
		RegistryFile, len(report.Registered), len(nextNames), len(added), len(removed))
	env.Printf("  生效：重编译 + 重启后端（接线真相在编译产物；本次重放不改 DB 台账与进程内注册表）\n")
	return nil
}

// syncRemoveReason 撤行理由（对账要能解释每一行的去留）
func syncRemoveReason(report *listReport, name string) string {
	item := report.Items[name]
	if item == nil {
		return "仅文件面存在（台账与进程内都没有该名）"
	}
	switch {
	case item.Status == "":
		return "台账无行（接线悬空：装一次或保留撤行）"
	case item.Kind == "declarative":
		return "declarative 无 Go 包：写注册行 `go build` 必失败"
	case item.Status == host.StatusFailed:
		return "failed（半装）：该态注册行应为「无」（清理入口 uninstall --force）"
	default:
		return "台账重放不含该名"
	}
}
