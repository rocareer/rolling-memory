package l2

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/l2/ddlplan"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// 安装期的结构计划面（：装（新实体）/升级/复装/删表的 DDL 都由 CLI 侧编排、本引擎生成与执行）。
//
// 与 cli 的接线方式：引擎把自己注册进 **internal/plugin/l2/ddlplan** 的注册表（叶子包），cli 只依赖
// 那个包。为什么不直接让 cli import 本包：本包在演示/验收用例（`package l2`）里 import
// `internal/plugin/cli`，cli 反向 import 本包会构成 **import cycle not allowed in test**
// （cmd/go 的硬校验）。ddlplan 的包注释是这条约束的唯一说明处。
//
// 注册发生在 init（与 router.go 的 l2.Mount 同一条「接线真相在进程内注册表」思路）：任何链接了本包的
// 二进制（aigo / serve）在启动时即具备该能力；未链接时 cli 侧 fail-closed 报错，不静默跳过
// （静默跳过等于「安装成功但表没建」）。
func init() { ddlplan.Set(installPlanner{}) }

// installPlanner Planner 的引擎实现（无状态：连接由调用方按次传入，不持有全局句柄）
type installPlanner struct{}

// 编译期断言：实现与接口逐参一致（漂移在编译期就报，不留到运行时）
var _ ddlplan.Planner = installPlanner{}

// Build 按清单实体生成结构计划：先装配成与元数据行等价的行（物理表名取元数据侧的显式值），
// 再走与启动预检同一条校验链（Parse：宿主闭集 + 引擎增补规则）与同一份 DDL 生成器（BuildPlan）。
// 拒绝类变更（删列/改类型/改列名）由 BuildPlan 与计划一并返回，调用方负责"计划非空 + error"的展示与拒装。
func (installPlanner) Build(ctx context.Context, db *gorm.DB, plugin string, entity manifest.Entity, physicalTable string) (*Plan, error) {
	raw, err := json.Marshal(entity)
	if err != nil {
		return nil, fmt.Errorf("序列化实体 %s 的 spec 失败: %w", entity.Entity, err)
	}
	row := host.Entity{
		Plugin:        plugin,
		Name:          entity.Entity,
		TitleZh:       entity.TitleZh,
		TitleEn:       entity.TitleEn,
		PhysicalTable: physicalTable,
		Spec:          datatypes.JSON(raw),
	}
	spec, err := Parse(row)
	if err != nil {
		return nil, fmt.Errorf("实体 %s 的 spec 非法: %w", entity.Entity, err)
	}
	return New(db).BuildPlan(ctx, spec)
}

// Apply 逐条执行计划（每条独立事务、失败即停并逐条回报；「不吞错」）
func (installPlanner) Apply(ctx context.Context, db *gorm.DB, plan *Plan) ([]StatementResult, error) {
	return New(db).ApplyPlan(ctx, plan)
}
