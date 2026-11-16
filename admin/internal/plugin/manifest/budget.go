package manifest

import (
	"fmt"
	"regexp"
	"strings"
)

// identifierPattern 实体名与字段名的闭集标识符（与 host 侧「硬条款」同一形状：
// 列名逐字段复核，任何用户输入都不得进入标识符位置）
var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// 长度预算常量（「标识符长度预算（安装期硬校验）」与原文取值，门禁同口径）：
const (
	// nodeBudget：节点名 = "plugin/"(7) + 名 + "/"(1) + 实体 + "/"(1) + 动作（最长 "create"=6）
	// 受 admin_rules.name varchar(64) 约束 ⇒ len(名) + len(实体) ≤ 49。**与 DDL 归属无关**：
	// 菜单/节点行由安装器代写，migrations 自管的实体一样有节点名。
	nodeBudget = 49
	// engineIndexBudgetBase：**引擎管的表**（ddl_owner=engine，缺省）上的索引名受 PG 标识符
	// 63 字节约束。引擎索引名 = <前缀> + kind(3) + "_plugin_" + 名 + "_" + 实体 + "_" + 后缀；
	// 字段名参与后缀（字段名最长可到 63）⇒ 引擎靠「截断 + 8 位 sha256」兜底，**兜底形态本身**才是
	// 真正的下限：前缀 + 11("idx_plugin_") + 1 + 名 + 1 + 实体 + 8 = 前缀 + 21 + len(名) + len(实体)
	// ≤ 63 ⇒ len(名) + len(实体) ≤ 42 − len(前缀)。
	//
	// 与设计原文的偏差（显式登记，设计稿是唯一规格）：原文记 `≤ 46 − len(前缀)`，其反推用的是
	// weigh 索引的**自然名**（前缀 + 17 + N + E，该字面量本身也有 17/18 的偏差），漏了兜底形态的自身
	// 开销——N+E 落在 43..46 时，恒发的 deleted_at 索引自然名与截断名双双超 63，引擎侧 indexName
	// 只能 fail-closed 报错（internal/plugin/l2/ddl_test.go 钉住 43 报错），即该区间「能过安装期校验、
	// 却建不出表」。此处按**可生成性**这一基本事实收紧到 42；check-plugin-manifest.sh 同口径。
	engineIndexBudgetBase = 42
)

// NameEntityBudget 返回 len(名) + len(实体) 的上限：
//   - admin_rules.name varchar(64) 的约束（49）恒适用（节点名形态与 DDL 归属无关）；
//   - **引擎管的表**（ddl_owner=engine，缺省）另受PG 标识符 63 字节约束 ⇒ 42 − len(表前缀)
//     （见 engineIndexBudgetBase 的推导）；
//   - ddl_owner=migrations 的表结构（含索引名）由插件迁移自管 ⇒ 引擎不发任何索引名（：
//     「引擎只读元数据做渲染与 CRUD，不做任何 ALTER/DROP」），只剩 49。
//
// 取适用者的更紧者。默认前缀为空、engine ⇒ 上限 42。
func NameEntityBudget(prefix, ddlOwner string) int {
	budget := nodeBudget
	if ownerOf(ddlOwner) != ddlOwnerMigrations {
		if indexBudget := engineIndexBudgetBase - len(prefix); indexBudget < budget {
			budget = indexBudget
		}
	}
	return budget
}

// ddl_owner 取值（闭集 engine | migrations）
const (
	ddlOwnerEngine     = "engine"
	ddlOwnerMigrations = "migrations"
)

// ownerOf ddl_owner 的规范化取值：缺省与未知值一律按 engine（引擎管结构）
func ownerOf(ddlOwner string) string {
	if strings.TrimSpace(ddlOwner) == ddlOwnerMigrations {
		return ddlOwnerMigrations
	}
	return ddlOwnerEngine
}

// CheckNameEntityLength 安装期硬校验：插件名 + 实体名的联合长度必须落在预算内。
// 表前缀参与引擎索引名（<前缀>idx_plugin_<名>_<实体>_<后缀>）的字节数，故前缀过长会挤压实体名预算；
// ddl_owner=migrations 的表不受索引名约束（引擎不发索引名）。
func CheckNameEntityLength(name, entity, prefix, ddlOwner string) error {
	budget := NameEntityBudget(prefix, ddlOwner)
	if budget < 1 {
		return fmt.Errorf("表前缀 %q 过长（%d 字节）：已吃掉全部长度预算，无法容纳任何实体", prefix, len(prefix))
	}
	if total := len(name) + len(entity); total > budget {
		return fmt.Errorf("插件名 %q + 实体名 %q 的联合长度 %d 超过上限 %d（表前缀 %q，ddl_owner=%s：%s）",
			name, entity, total, budget, prefix, ownerOf(ddlOwner), budgetBasis(ownerOf(ddlOwner)))
	}
	return nil
}

// budgetBasis 预算依据的可读说明（错误信息用：让作者知道该改哪个名字/哪个约束在拦）
func budgetBasis(owner string) string {
	if owner == ddlOwnerMigrations {
		return "结构由插件迁移自管 ⇒ 引擎不发索引名，仅受 admin_rules.name varchar(64) 的节点名约束"
	}
	return fmt.Sprintf("取 admin_rules.name varchar(64) 的 %d 与引擎索引名 PG 标识符 63 字节的 %d−len(表前缀) 的更紧者",
		nodeBudget, engineIndexBudgetBase)
}
