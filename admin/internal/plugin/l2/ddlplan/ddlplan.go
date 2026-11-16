// Package ddlplan 是「安装期 L2 实体结构（DDL）」在 **CLI** 与 **L2 引擎**之间的最小接口面。
//
// 为什么要单独一个叶子包（而不是让 cli 直接 import internal/plugin/l2）：L2 的演示/验收用例在
// `package l2` 内 import `internal/plugin/cli`（端到端要经 CLI 的 install/uninstall/enable 链路），
// 若 cli 反过来 import l2，测试二进制就是 **import cycle not allowed in test**（cmd/go 的硬校验）。
// 故：引擎把自己注册进本包（l2 → ddlplan，见 internal/plugin/l2 的 planner.go），cli 只依赖本包；
// 两个方向不再对向依赖，而类型仍只有一份（l2 侧用类型别名指向本包，不另立一套结构）。
//
// 本包是**叶子**：只依赖 context / gorm / manifest；既不 import l2 也不 import cli。
// 设计说明（DDL 生成与执行）与硬条款
// （标识符闭集、值参数化、失败即停）——本包只承载接口与读数形态，这些条款的落地在引擎里。
package ddlplan

import (
	"context"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/gorm"
)

// Statement 一条待执行 DDL（kind 供 dry-run 分类展示与失败定位）
type Statement struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Result 逐条 DDL 的执行回报（失败即停：切片含失败那条与之前已成功的条目）
type Result struct {
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
}

// Plan 某实体的结构计划（spec 为目标态，**实际表结构自省**为基线）
type Plan struct {
	Plugin string `json:"plugin"`
	Entity string `json:"entity"`
	Table  string `json:"table"`
	// DDLOwner engine | migrations（migrations 时 Statements 恒为空）
	DDLOwner string `json:"ddl_owner"`
	// Existing 表已存在（false ⇒ 计划里是 CREATE TABLE）
	Existing bool `json:"existing"`
	// Statements 按执行顺序（建表 → 索引 → 注释；既有表则只含缺失的加列/放宽/补索引）
	Statements []Statement `json:"statements"`
	// Warnings 报告项（保留列、属性差异、零值默认补位等；不由引擎自动改）
	Warnings []string `json:"warnings,omitempty"`
	// KeptColumns 表里存在而 spec 未声明的列（一律保留，只进报告，不自动 DROP）
	KeptColumns []string `json:"kept_columns,omitempty"`
}

// Planner 引擎的结构计划面（安装期 DDL 的全部动作都经它；cli 拿不到、也不需要引擎的其余内部）
type Planner interface {
	// Build 生成某清单实体的结构计划：physicalTable 为元数据侧的物理表名（显式存值）。
	// 拒绝类变更（删列/改类型/改列名等）经 error 返回，计划可能非空（dry-run 要把它列全）。
	Build(ctx context.Context, db *gorm.DB, plugin string, entity manifest.Entity, physicalTable string) (*Plan, error)
	// Apply 逐条执行计划（每条独立事务、失败即停并逐条回报，「不吞错」）
	Apply(ctx context.Context, db *gorm.DB, plan *Plan) ([]Result, error)
}

var (
	mu      sync.RWMutex
	planner Planner
)

// Set 注册引擎实现（由 internal/plugin/l2 的 init 调用）。重复注册覆盖（测试可换实现）。
func Set(p Planner) {
	mu.Lock()
	defer mu.Unlock()
	planner = p
}

// Get 取引擎实现：未接入（本二进制没有链接 internal/plugin/l2）⇒ nil。
// 调用方必须 fail-closed 报错，不静默跳过——静默跳过等于「安装成功但表没建」（否决项）。
func Get() Planner {
	mu.RLock()
	defer mu.RUnlock()
	return planner
}
