// plugin: kb v1.0.0
// 知识库维护面服务（运维端点的服务侧落点）：
//   - LiveLibraryIDs 存活库清单（重投嵌入的作用域枚举；软删库不参与）；
//   - EnqueueReembed 按当前嵌入身份逐库投递重嵌作业（kb:rebuild mode=reembed；同键单飞）。
//
// 与「逐库作业」的关系：重嵌是**库级**作业（kb:rebuild 协调行按水位展开为逐文档重导），
// 所以这里枚举库并逐库投递，而不是造一条跨库作业——键口径与作业面既有语义一致
// （kb:rebuild:<library_id>:auto；build 传 auto = 由作业内复算产物身份）。
package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	kbembed "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/embed"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// ReembedJob 一份重嵌作业的回执行
type ReembedJob struct {
	LibraryID uint   `json:"library_id"`
	JobID     uint64 `json:"job_id"`
	JobKey    string `json:"job_key"`
	Created   bool   `json:"created"` // false = 同键活跃作业已在飞（单飞）
}

// ReembedReport 重投嵌入回执
type ReembedReport struct {
	Identity     string       `json:"identity"`      // 当前嵌入身份（重产向量的身份基准）
	Count        int          `json:"count"`         // 投递（含已在飞）的作业数
	CreatedCount int          `json:"created_count"` // 其中新建的作业数
	Jobs         []ReembedJob `json:"jobs"`
}

// LiveLibraryIDs 存活库 id 清单（软删库不可见；按 id 升序稳定）
func (s *Service) LiveLibraryIDs(ctx context.Context) ([]uint, error) {
	if s.db == nil {
		return nil, errors.New("kb: 数据库未初始化")
	}
	ids := make([]uint, 0, 8)
	if err := gorm.G[model.KBLibrary](s.db).Select("id").Order("id ASC").Scan(ctx, &ids); err != nil {
		return nil, fmt.Errorf("kb: 读存活库清单失败: %w", err)
	}
	return ids, nil
}

// EnqueueReembed 按当前嵌入身份投递重嵌作业；libraryID=0 = 全部存活库。
//
// 身份前置（fail-closed）：身份不可用（Provider 未注册或模型未配置）⇒ 响亮失败——
// 没有身份就没有向量空间的基准，投递出去的作业也会在嵌入阶段失败；这里直接拦在入口。
func (s *Service) EnqueueReembed(ctx context.Context, libraryID uint) (*ReembedReport, error) {
	identity := strings.TrimSpace(kbembed.ModelIdentity())
	if identity == "" {
		return nil, errors.New("kb: 嵌入模型身份不可用（Provider 未注册或模型未配置）——无法重产向量")
	}

	var ids []uint
	if libraryID > 0 {
		ids = []uint{libraryID}
	} else {
		live, err := s.LiveLibraryIDs(ctx)
		if err != nil {
			return nil, err
		}
		ids = live
	}
	if len(ids) == 0 {
		return &ReembedReport{Identity: identity, Jobs: []ReembedJob{}}, nil
	}

	report := &ReembedReport{Identity: identity, Jobs: make([]ReembedJob, 0, len(ids))}
	for _, id := range ids {
		job, created, err := s.EnqueueRebuildLibrary(ctx, id, ingest.RebuildModeReembed, "")
		if err != nil {
			return nil, fmt.Errorf("kb: 库 %d 投递重嵌作业失败: %w", id, err)
		}
		row := ReembedJob{LibraryID: id, Created: created}
		if job != nil {
			row.JobID, row.JobKey = job.ID, job.Key
		}
		if created {
			report.CreatedCount++
		}
		report.Jobs = append(report.Jobs, row)
	}
	report.Count = len(report.Jobs)
	return report, nil
}

// DB 服务持有的库句柄（运维端点做只读探测时复用同一句柄）
func (s *Service) DB() *gorm.DB { return s.db }
