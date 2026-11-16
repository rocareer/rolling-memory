package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pluginSource "github.com/ai-go-hub/ai-go-admin/internal/plugin/source"

	"github.com/spf13/cobra"
	"gorm.io/gorm"
)

// SourceCommand `aigo plugin source` 命令组（来源 CRUD / 启停 / 只读测试）。
func SourceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "仓库来源配置（local/http 共用一种 catalog 格式）",
	}
	cmd.AddCommand(sourceListCmd(), sourceShowCmd(), sourceAddCmd(), sourceUpdateCmd(),
		sourceRemoveCmd(), sourceEnableCmd(), sourceDisableCmd(), sourceTestCmd())
	return cmd
}

// CatalogCommand `aigo plugin catalog` 命令组（同步 / 列举 / 预演 / 安装）。
func CatalogCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "统一插件清单（ai-go-plugin-catalog/v1）：同步、列举、预演与安装",
	}
	cmd.AddCommand(catalogSyncCmd(), catalogListCmd(), catalogPreflightCmd(),
		catalogInstallCmd(), catalogVerifyCmd())
	return cmd
}

func sourceStore(env *Env) pluginSource.Store {
	return pluginSource.NewGormStore(func(ctx context.Context) (*gorm.DB, error) {
		return env.EnsureDB(ctx)
	})
}

func sourceListCmd() *cobra.Command {
	var jsonOut bool
	var page, limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出来源（最后同步状态 / 清单摘要）",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			rows, total, err := sourceStore(env).ListSources(cmd.Context(), pluginSource.ListSourcesQuery{Page: page, Limit: limit})
			if err != nil {
				return fmt.Errorf("列出来源失败: %w", err)
			}
			if jsonOut {
				return printJSON(map[string]any{"list": rows, "total": total})
			}
			for _, row := range rows {
				status := row.LastSyncStatus
				if status == "" {
					status = "never"
				}
				fmt.Fprintf(env.Writer(), "%-24s %-5s %-8s %-8s %s\n", row.Name, row.Type, status, shortSHA(row.LastCatalogSHA256), row.Location)
			}
			fmt.Fprintf(env.Writer(), "共 %d 条\n", total)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON 输出")
	cmd.Flags().IntVar(&page, "page", 1, "页码")
	cmd.Flags().IntVar(&limit, "limit", 100, "每页条数")
	return cmd
}

func sourceShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "查看单个来源（含最后同步错误/清单摘要）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			row, err := sourceStore(env).GetSourceByName(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(row)
			}
			fmt.Fprintf(env.Writer(), "name:           %s\ntitle:          %s\ntype:           %s\nlocation:       %s\nenabled:        %d\nweigh:          %d\nlast_status:    %s\nlast_sha256:    %s\nlast_synced_at: %s\nlast_error:     %s\n",
				row.Name, row.Title, row.Type, row.Location, row.Enabled, row.Weigh,
				orNever(row.LastSyncStatus), row.LastCatalogSHA256, sourceFormatTime(row.LastSyncedAt), row.LastSyncError)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON 输出")
	return cmd
}

func sourceAddCmd() *cobra.Command {
	var in pluginSource.SourceInput
	var disabled, allowInsecure, allowPrivate, allowAbsolute bool
	var weigh int64
	var operator string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "新增来源（只做词法校验，不联网）",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			in.Type = defaultString(in.Type, pluginSource.TypeLocal)
			enabled := int16(1)
			if disabled {
				enabled = 0
			}
			in.Enabled = &enabled
			inert, private, absolute := int16(0), int16(0), int16(0)
			if allowInsecure {
				inert = 1
			}
			if allowPrivate {
				private = 1
			}
			if allowAbsolute {
				absolute = 1
			}
			in.AllowInsecureHTTP = &inert
			in.AllowPrivateHosts = &private
			in.AllowAbsolutePaths = &absolute
			in.Weigh = &weigh
			src, err := pluginSource.NewSource(in, defaultOperator(operator))
			if err != nil {
				return err
			}
			if err := sourceStore(env).CreateSource(cmd.Context(), src); err != nil {
				return err
			}
			fmt.Fprintf(env.Writer(), "✓ 已新增来源 %s（%s）\n", src.Name, src.Type)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.Name, "name", "", "来源键（^[a-z][a-z0-9-]{1,31}$；创建后不可改）")
	cmd.Flags().StringVar(&in.Title, "title", "", "展示名")
	cmd.Flags().StringVar(&in.Type, "type", pluginSource.TypeLocal, "运输方式：local|http")
	cmd.Flags().StringVar(&in.Location, "location", "", "本地路径（相对仓库根）或 https URL")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "创建为停用")
	cmd.Flags().BoolVar(&allowInsecure, "allow-insecure-http", false, "允许明文 http（仅 http 来源）")
	cmd.Flags().BoolVar(&allowPrivate, "allow-private-hosts", false, "允许私网/回环主机（仅 http 来源）")
	cmd.Flags().BoolVar(&allowAbsolute, "allow-absolute-paths", false, "允许本地绝对路径（仅 local 来源）")
	cmd.Flags().Int64Var(&weigh, "weigh", 0, "排序权重（大在前）")
	cmd.Flags().StringVar(&in.Remark, "remark", "", "备注")
	cmd.Flags().StringVar(&operator, "operator", "", "操作者署名（审计）")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("location")
	return cmd
}

func sourceUpdateCmd() *cobra.Command {
	var title, typ, location, remark, operator string
	var enable, disable, allowInsecure, allowPrivate, allowAbsolute bool
	var weigh int64
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "修改来源（name 不可改；type/location/安全开关变化会清空快照）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			store := sourceStore(env)
			row, err := store.GetSourceByName(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			candidate := *row
			patch := pluginSource.SourcePatch{UpdatedBy: defaultOperator(operator)}
			if cmd.Flags().Changed("title") {
				candidate.Title = title
				patch.Title = &title
			}
			if cmd.Flags().Changed("type") {
				candidate.Type = typ
				patch.Type = &typ
			}
			if cmd.Flags().Changed("location") {
				candidate.Location = location
				patch.Location = &location
			}
			if cmd.Flags().Changed("remark") {
				candidate.Remark = remark
				patch.Remark = &remark
			}
			if cmd.Flags().Changed("weigh") {
				candidate.Weigh = weigh
				patch.Weigh = &weigh
			}
			enabled := candidate.Enabled
			if cmd.Flags().Changed("enable") || cmd.Flags().Changed("disable") {
				if enable && disable {
					return errors.New("--enable 与 --disable 只能选一个")
				}
				if enable {
					enabled = 1
				}
				if disable {
					enabled = 0
				}
				candidate.Enabled = enabled
				patch.Enabled = &enabled
			}
			if cmd.Flags().Changed("allow-insecure-http") || cmd.Flags().Changed("allow-private-hosts") || cmd.Flags().Changed("allow-absolute-paths") {
				if cmd.Flags().Changed("allow-insecure-http") {
					value := int16(0)
					if allowInsecure {
						value = 1
					}
					candidate.AllowInsecureHTTP = value
					patch.AllowInsecureHTTP = &value
				}
				if cmd.Flags().Changed("allow-private-hosts") {
					value := int16(0)
					if allowPrivate {
						value = 1
					}
					candidate.AllowPrivateHosts = value
					patch.AllowPrivateHosts = &value
				}
				if cmd.Flags().Changed("allow-absolute-paths") {
					value := int16(0)
					if allowAbsolute {
						value = 1
					}
					candidate.AllowAbsolutePaths = value
					patch.AllowAbsolutePaths = &value
				}
			}
			if err := candidate.NormalizeAndValidate(); err != nil {
				return err
			}
			// 规范化可能把与类型无关的开关清零：把规范化后的值回填到 patch，避免 store 写入旧值。
			if candidate.Type == pluginSource.TypeLocal {
				zero := int16(0)
				patch.AllowInsecureHTTP = &zero
				patch.AllowPrivateHosts = &zero
			} else {
				zero := int16(0)
				patch.AllowAbsolutePaths = &zero
			}
			patch.ClearSnapshot = sourceSnapshotAffecting(cmd) || sourceSecurityFlagsChanged(cmd)
			cleared, err := store.UpdateSource(cmd.Context(), row.ID, patch)
			if err != nil {
				return err
			}
			fmt.Fprintf(env.Writer(), "✓ 已更新来源 %s（快照清空：%v）\n", row.Name, cleared)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "展示名")
	cmd.Flags().StringVar(&typ, "type", "", "运输方式：local|http")
	cmd.Flags().StringVar(&location, "location", "", "本地路径或 URL")
	cmd.Flags().BoolVar(&enable, "enable", false, "启用")
	cmd.Flags().BoolVar(&disable, "disable", false, "停用")
	cmd.Flags().BoolVar(&allowInsecure, "allow-insecure-http", false, "允许明文 http")
	cmd.Flags().BoolVar(&allowPrivate, "allow-private-hosts", false, "允许私网/回环主机")
	cmd.Flags().BoolVar(&allowAbsolute, "allow-absolute-paths", false, "允许本地绝对路径")
	cmd.Flags().Int64Var(&weigh, "weigh", 0, "排序权重")
	cmd.Flags().StringVar(&remark, "remark", "", "备注")
	cmd.Flags().StringVar(&operator, "operator", "", "操作者署名")
	return cmd
}

func sourceSnapshotAffecting(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("type") || cmd.Flags().Changed("location")
}

func sourceSecurityFlagsChanged(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("allow-insecure-http") ||
		cmd.Flags().Changed("allow-private-hosts") ||
		cmd.Flags().Changed("allow-absolute-paths")
}

func sourceRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "删除来源（同事务删除清单快照；不影响已安装插件）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			store := sourceStore(env)
			row, err := store.GetSourceByName(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(env.Writer(), "将删除来源 %s（id=%d，location=%s）及其清单快照；已安装插件不受影响。加 --yes 确认。\n",
					row.Name, row.ID, row.Location)
				return errors.New("缺少 --yes，已拒绝删除")
			}
			deleted, err := store.DeleteSources(cmd.Context(), []uint{row.ID})
			if err != nil {
				return err
			}
			fmt.Fprintf(env.Writer(), "✓ 已删除 %d 个来源\n", deleted)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "确认删除")
	return cmd
}

func sourceEnableCmd() *cobra.Command  { return sourceStatusCmd(true) }
func sourceDisableCmd() *cobra.Command { return sourceStatusCmd(false) }

func sourceStatusCmd(enable bool) *cobra.Command {
	var operator string
	action := "enable"
	short := "启用来源"
	if !enable {
		action = "disable"
		short = "停用来源"
	}
	cmd := &cobra.Command{
		Use:   action + " <name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			store := sourceStore(env)
			row, err := store.GetSourceByName(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			value := int16(0)
			if enable {
				value = 1
			}
			if _, err := store.UpdateSource(cmd.Context(), row.ID, pluginSource.SourcePatch{Enabled: &value, UpdatedBy: defaultOperator(operator)}); err != nil {
				return err
			}
			fmt.Fprintf(env.Writer(), "✓ 来源 %s 已%s\n", row.Name, map[bool]string{true: "启用", false: "停用"}[enable])
			return nil
		},
	}
	cmd.Flags().StringVar(&operator, "operator", "", "操作者署名")
	return cmd
}

func sourceTestCmd() *cobra.Command {
	var typ, location string
	var allowInsecure, allowPrivate, allowAbsolute, jsonOut bool
	cmd := &cobra.Command{
		Use:   "test",
		Short: "只读测试：读取并严格解析清单（不写库）",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			src := &pluginSource.Source{Type: defaultString(typ, pluginSource.TypeLocal), Location: location}
			if allowInsecure {
				src.AllowInsecureHTTP = 1
			}
			if allowPrivate {
				src.AllowPrivateHosts = 1
			}
			if allowAbsolute {
				src.AllowAbsolutePaths = 1
			}
			result := pluginSource.TestSource(cmd.Context(), src, env.Root)
			if jsonOut {
				if err := printJSON(result); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(env.Writer(), "ok:      %v\nschema:  %s\nentries: %d\nsha256:  %s\ngenerated_at: %s\n",
					result.OK, result.Schema, result.Entries, result.CatalogSHA256, result.GeneratedAt)
				for _, item := range result.Errors {
					fmt.Fprintf(env.Writer(), "error:   %s\n", item)
				}
			}
			if !result.OK {
				return errors.New("来源测试未通过")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", pluginSource.TypeLocal, "运输方式：local|http")
	cmd.Flags().StringVar(&location, "location", "", "本地路径或 URL")
	cmd.Flags().BoolVar(&allowInsecure, "allow-insecure-http", false, "允许明文 http")
	cmd.Flags().BoolVar(&allowPrivate, "allow-private-hosts", false, "允许私网/回环主机")
	cmd.Flags().BoolVar(&allowAbsolute, "allow-absolute-paths", false, "允许本地绝对路径")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON 输出")
	_ = cmd.MarkFlagRequired("location")
	return cmd
}

func catalogSyncCmd() *cobra.Command {
	var dryRun, jsonOut bool
	cmd := &cobra.Command{
		Use:   "sync [来源名...]",
		Short: "同步清单快照（缺省同步所有 enabled 来源；--dry-run 只读解析）",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			store := sourceStore(env)
			sources, err := catalogSyncTargets(cmd.Context(), store, args)
			if err != nil {
				return err
			}
			if len(sources) == 0 {
				return errors.New("没有需要同步的来源")
			}
			results := make([]pluginSource.SyncResult, 0, len(sources))
			failed := 0
			for i := range sources {
				result := pluginSource.SyncSource(cmd.Context(), store, &sources[i], env.Root, dryRun)
				results = append(results, result)
				if result.Status != "ok" {
					failed++
				}
			}
			if jsonOut {
				if err := printJSON(map[string]any{"results": results, "partial": failed > 0 && failed < len(results)}); err != nil {
					return err
				}
			} else {
				for _, result := range results {
					if result.Status == "ok" {
						fmt.Fprintf(env.Writer(), "✓ %-24s entries=%d sha256=%s\n", result.Name, result.Entries, shortSHA(result.CatalogSHA256))
					} else {
						fmt.Fprintf(env.Writer(), "✗ %-24s %s\n", result.Name, result.Error)
					}
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d 个来源同步失败", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只读解析，不写快照")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON 输出")
	return cmd
}

func catalogSyncTargets(ctx context.Context, store pluginSource.Store, names []string) ([]pluginSource.Source, error) {
	if len(names) > 0 {
		out := make([]pluginSource.Source, 0, len(names))
		for _, name := range names {
			row, err := store.GetSourceByName(ctx, name)
			if err != nil {
				return nil, err
			}
			out = append(out, *row)
		}
		return out, nil
	}
	rows, err := pluginSource.ListAllSources(ctx, store)
	if err != nil {
		return nil, err
	}
	out := make([]pluginSource.Source, 0, len(rows))
	for i := range rows {
		if rows[i].Enabled == 1 {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// catalogListOptions 清单列举的旗标取值（过滤 / 分页 / 输出形态）。
type catalogListOptions struct {
	Source  string
	UID     string
	Version string
	Page    int
	Limit   int
	JSON    bool
}

func catalogListCmd() *cobra.Command {
	var opts catalogListOptions
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列举清单快照（标记已安装/同内容/来源冲突）",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			return runCatalogList(cmd.Context(), env, sourceStore(env), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Source, "source", "", "按来源名过滤")
	cmd.Flags().StringVar(&opts.UID, "uid", "", "按 uid 过滤")
	cmd.Flags().StringVar(&opts.Version, "version", "", "按版本过滤")
	cmd.Flags().IntVar(&opts.Page, "page", 1, "页码")
	cmd.Flags().IntVar(&opts.Limit, "limit", 40, "每页条数")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "JSON 输出")
	return cmd
}

// runCatalogList 执行清单列举：把过滤 / 分页选项编译成 ListEntriesQuery 交给 store，再渲染表格或 JSON。
//
// 与命令侧分离是**测试 seam**（同包 PreflightCatalog / catalogSyncTargets 的既有惯例）：命令侧固定
// NewSourceEnv() + sourceStore(env)，用例无法注入替身；而「过滤条件被静默丢弃」正是这类查询的已知缺陷
// 形态（见 internal/plugin/source/store.go 的 ListEntries 注释：泛型入口的 NewDB 会话会丢预挂的 Where），
// 需要一条钉住「旗标 → query 传参没被丢」的用例（TestCatalogListQueryPropagation）。
// 本函数与拆分前的 RunE 逐行等价（行为不变）。SQL 层「真过滤」由 internal/plugin/source 的库级用例承担。
func runCatalogList(ctx context.Context, env *Env, store pluginSource.Store, opts catalogListOptions) error {
	query := pluginSource.ListEntriesQuery{UID: opts.UID, Version: opts.Version, Page: opts.Page, Limit: opts.Limit}
	if opts.Source != "" {
		row, err := store.GetSourceByName(ctx, opts.Source)
		if err != nil {
			return err
		}
		query.SourceID = row.ID
	}
	rows, total, err := store.ListEntries(ctx, query)
	if err != nil {
		return err
	}
	uids := make([]string, 0, len(rows))
	pairs := make([]pluginSource.UIDVersion, 0, len(rows))
	for i := range rows {
		uids = append(uids, rows[i].UID)
		pairs = append(pairs, pluginSource.UIDVersion{UID: rows[i].UID, Version: rows[i].Version})
	}
	installed, err := store.InstalledVersions(ctx, uids)
	if err != nil {
		return err
	}
	peers, err := store.ListEntryPeers(ctx, pairs)
	if err != nil {
		return err
	}
	flags := pluginSource.EntryPeerFlags(rows, peers)
	if opts.JSON {
		type row struct {
			pluginSource.CatalogEntry
			Installed      *pluginSource.InstalledPlugin `json:"installed,omitempty"`
			SameContent    bool                          `json:"same_content"`
			SourceConflict bool                          `json:"source_conflict"`
		}
		list := make([]row, 0, len(rows))
		for i := range rows {
			item := row{CatalogEntry: rows[i]}
			if installedRow, ok := installed[rows[i].UID]; ok {
				item.Installed = &installedRow
			}
			flag := flags[pluginSource.EntryRowKey(rows[i])]
			item.SameContent = flag.SameContent
			item.SourceConflict = flag.SourceConflict
			list = append(list, item)
		}
		return printJSON(map[string]any{"list": list, "total": total})
	}
	for i := range rows {
		installedNote := ""
		if installedRow, ok := installed[rows[i].UID]; ok {
			installedNote = "已安装 " + installedRow.Version
		}
		conflictNote := ""
		flag := flags[pluginSource.EntryRowKey(rows[i])]
		if flag.SourceConflict {
			conflictNote = "来源冲突"
		} else if flag.SameContent {
			conflictNote = "同内容多来源"
		}
		fmt.Fprintf(env.Writer(), "%-24s %-10s %-8s %-12s %-10s %s %s\n",
			rows[i].UID, rows[i].Version, rows[i].Kind, rows[i].SourceName, shortSHA(rows[i].PackageSHA256), installedNote, conflictNote)
	}
	fmt.Fprintf(env.Writer(), "共 %d 条\n", total)
	return nil
}

func catalogPreflightCmd() *cobra.Command {
	var sourceName, operator string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "preflight <uid>@<version> --source <来源名>",
		Short: "非破坏预演（复用 install --dry-run 全量管线；不写库/不落盘）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCatalogPreflight(cmd, args[0], sourceName, operator, jsonOut)
		},
	}
	cmd.Flags().StringVar(&sourceName, "source", "", "来源名（始终必填，避免多来源静默选源）")
	cmd.Flags().StringVar(&operator, "operator", "", "操作者署名")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON 输出")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func catalogVerifyCmd() *cobra.Command {
	var sourceName, operator string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "verify <uid>@<version> --source <来源名>",
		Short: "preflight 的别名（脚本语义）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCatalogPreflight(cmd, args[0], sourceName, operator, jsonOut)
		},
	}
	cmd.Flags().StringVar(&sourceName, "source", "", "来源名")
	cmd.Flags().StringVar(&operator, "operator", "", "操作者署名")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON 输出")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func runCatalogPreflight(cmd *cobra.Command, target, sourceName, operator string, jsonOut bool) error {
	uid, version, err := splitTarget(target)
	if err != nil {
		return err
	}
	env, err := NewSourceEnv()
	if err != nil {
		return err
	}
	store := sourceStore(env)
	row, err := store.GetSourceByName(cmd.Context(), sourceName)
	if err != nil {
		return err
	}
	result, err := PreflightCatalog(cmd.Context(), env, store, row.ID, uid, version, defaultOperator(operator))
	if err != nil {
		return err
	}
	if jsonOut {
		if err := printJSON(result); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(env.Writer(), result.Report)
		fmt.Fprintf(env.Writer(), "installable=%v reason=%s signature_present=%v trusted_keys_configured=%v allow_unsigned_required=%v\n",
			result.Installable, result.Reason, result.SignaturePresent, result.TrustedKeysConfigured, result.AllowUnsignedRequired)
		if result.ErrorCode != "" {
			fmt.Fprintf(env.Writer(), "error_code=%s\n", result.ErrorCode)
		}
	}
	if !result.Installable {
		reason := result.Reason
		if reason == "" {
			reason = "unknown"
		}
		return fmt.Errorf("不可安装: %s", reason)
	}
	return nil
}

func catalogInstallCmd() *cobra.Command {
	var sourceName, operator, confirmSHA string
	var allowUnsigned, dryRun, jsonOut bool
	cmd := &cobra.Command{
		Use:   "install <uid>@<version> --source <来源名>",
		Short: "从清单安装（复用既有安装链；不提供 --force/覆盖，v1 不支持升级）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid, version, err := splitTarget(args[0])
			if err != nil {
				return err
			}
			env, err := NewSourceEnv()
			if err != nil {
				return err
			}
			store := sourceStore(env)
			row, err := store.GetSourceByName(cmd.Context(), sourceName)
			if err != nil {
				return err
			}
			if dryRun {
				return runCatalogPreflight(cmd, args[0], sourceName, operator, jsonOut)
			}
			if confirmSHA == "" {
				preview, err := PreflightCatalog(cmd.Context(), env, store, row.ID, uid, version, defaultOperator(operator))
				if err != nil {
					return err
				}
				if !preview.Installable {
					return fmt.Errorf("预演未通过（%s），拒绝安装", preview.Reason)
				}
				confirmSHA = preview.Package.SHA256
			}
			result, err := InstallCatalog(cmd.Context(), env, store, CatalogInstallRequest{
				SourceID:      row.ID,
				UID:           uid,
				Version:       version,
				AllowUnsigned: allowUnsigned,
				ConfirmSHA256: confirmSHA,
				Operator:      defaultOperator(operator),
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(result)
			}
			fmt.Fprintf(env.Writer(), "✓ 已安装 %s v%s（status=%s, effect=%s）\n", result.Name, result.Version, result.Status, result.Effect)
			fmt.Fprintf(env.Writer(), "  台账来源：%s（catalog 来源 %s）\n", result.Source, sourceName)
			fmt.Fprintf(env.Writer(), "  checksum：%s\n", result.Checksum)
			if result.SignedBy != "" {
				fmt.Fprintf(env.Writer(), "  签名指纹：%s\n", result.SignedBy)
			}
			fmt.Fprintf(env.Writer(), "  生效方式：重编译 + 重启后端（effect=rebuild_required）；启用：aigo plugin enable %s\n", result.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&sourceName, "source", "", "来源名（始终必填）")
	cmd.Flags().StringVar(&operator, "operator", "", "操作者署名")
	cmd.Flags().StringVar(&confirmSHA, "confirm-sha256", "", "预演回执里的包 sha256（缺省自动预演取当前值）")
	cmd.Flags().BoolVar(&allowUnsigned, "allow-unsigned", false, "接受未签名/本机无法验证的包（仅本次安装）")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "等价 preflight")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON 输出")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func splitTarget(target string) (string, string, error) {
	uid, version, found := strings.Cut(strings.TrimSpace(target), "@")
	if !found || uid == "" || version == "" {
		return "", "", fmt.Errorf("目标 %q 不是 uid@version 形式", target)
	}
	return uid, version, nil
}

func printJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func defaultOperator(value string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return "cli"
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func orNever(value string) string {
	if value == "" {
		return "never"
	}
	return value
}

func sourceFormatTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Format(time.RFC3339)
}
