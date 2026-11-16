// Package version 提供版本与构建信息
//
// 版本号唯一事实源为仓库根目录 VERSION 文件，构建时嵌入（embed_version.go）；
// 提交哈希、提交时间与工作区修改标记取自 Go 编译器的 VCS 戳记（debug.ReadBuildInfo），
// 因此 air、go build 等任意构建方式均自动携带正确的提交信息。
// 分支标识无法从 VCS 戳记获得，由构建入口经 ldflags 注入，未注入时为空，展示层优雅降级。
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	assets "github.com/ai-go-hub/ai-go-admin"
)

// Info 版本信息
type Info struct {
	Version    string `json:"version"`     // 版本号 X.Y.Z（VERSION 文件）
	Branch     string `json:"branch"`      // 构建时注入的分支标识，未注入为空
	Commit     string `json:"commit"`      // 构建来源提交的短哈希，无 VCS 戳记时为空
	CommitTime string `json:"commit_time"` // 提交时间（RFC3339），无 VCS 戳记时为空
	Modified   bool   `json:"modified"`    // 构建时工作区是否存在未提交修改
	GoVersion  string `json:"go_version"`  // 构建所用的 Go 工具链版本
}

var (
	once sync.Once
	info Info

	// buildBranch 构建时的分支标识，由 ldflags -X 注入；未注入时为空。
	buildBranch string
)

// Get 获取版本信息（进程内只读取一次）
func Get() Info {
	once.Do(func() {
		info = Info{Version: readVersionFile(), Branch: strings.TrimSpace(buildBranch), GoVersion: runtime.Version()}

		buildInfo, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		for _, setting := range buildInfo.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Commit = setting.Value
				if len(info.Commit) > 7 {
					info.Commit = info.Commit[:7]
				}
			case "vcs.time":
				info.CommitTime = setting.Value
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	})
	return info
}

// CommitAt 返回本地时区的提交时间（如 2026-10-18 15:29:02 +08:00），无信息时为空
func (i Info) CommitAt() string {
	if i.CommitTime == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, i.CommitTime)
	if err != nil {
		return i.CommitTime
	}
	return t.Local().Format("2006-01-02 15:04:05 -07:00")
}

// readVersionFile 读取构建时嵌入的 VERSION 文件
func readVersionFile() string {
	raw, err := assets.VersionFS.ReadFile("VERSION")
	if err != nil {
		return "dev"
	}
	if v := strings.TrimSpace(string(raw)); v != "" {
		return v
	}
	return "dev"
}
