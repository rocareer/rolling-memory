// plugin: kb v1.0.0
// 知识库巡检面（团队路：悬空/回收点名；运维巡检端点的服务侧实现）。
//
// 只读：不改任何行、不自动删授权。三类点名：
//   - dangling（悬空）：kb_team 绑定的组名在 user_groups 已不存在（改名/删除）——相关授权
//     对一切用户不可命中（读视图 teams(u) 走组名 → 无人生成该 team id），fail-closed 已生效，
//     但授权行仍在 → 点名待人工重绑；「团队禁用/已删」同理不参与判定。
//   - recycled（回收）：组名存在但**现行最早命中组 id ≠ 绑定时 bound_group_id**——旧组名被
//     回收（改名后新建同名组），新组会静默继承历史授权 → 点名 + 重绑确认兜底。
//   - orphan grants（悬空授权）：subject_kind='team' 的活跃授权行指向不存在/已删的 kb_team。
package kb

import (
	"context"
	"fmt"
	"sort"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// 巡检结论档（团队绑定）闭集
const (
	CheckKindOK       = "ok"       // 绑定正常
	CheckKindDisabled = "disabled" // 团队映射禁用（不参与读视图；授权保留）
	CheckKindDangling = "dangling" // 组名不存在（悬空；fail-closed 已生效）
	CheckKindRecycled = "recycled" // 组名被回收（现行最早命中 id ≠ 绑定时 id）
)

// TeamFinding 单个团队绑定的巡检结论
type TeamFinding struct {
	TeamID         uint   `json:"team_id"`
	GroupName      string `json:"group_name"`
	DisplayName    string `json:"display_name"`
	BoundGroupID   uint   `json:"bound_group_id"`   // 绑定时 FindByName 最早命中 id（0=未记录基线）
	CurrentGroupID uint   `json:"current_group_id"` // 现行 FindByName 最早命中 id（0=无同名组）
	Kind           string `json:"kind"`             // ok|disabled|dangling|recycled
	Hint           string `json:"hint"`
}

// OrphanTeamGrant 悬空授权行（指向不存在/已删团队的 team 授权）
type OrphanTeamGrant struct {
	GrantID   uint `json:"grant_id"`
	LibraryID uint `json:"library_id"`
	TeamID    uint `json:"team_id"`
}

// CheckReport 巡检报告（aigo kb check 输出载体）
type CheckReport struct {
	Teams            []TeamFinding     `json:"teams"`
	OrphanTeamGrants []OrphanTeamGrant `json:"orphan_team_grants"`
}

// Issues 点名条数（悬空 + 回收 + 悬空授权；ok/disabled 不计）
func (r *CheckReport) Issues() int {
	n := len(r.OrphanTeamGrants)
	for i := range r.Teams {
		if r.Teams[i].Kind == CheckKindDangling || r.Teams[i].Kind == CheckKindRecycled {
			n++
		}
	}
	return n
}

// CheckTeams 团队绑定巡检（只读、可复跑；口径见文件头）。
func (s *Service) CheckTeams(ctx context.Context) (*CheckReport, error) {
	report := &CheckReport{Teams: []TeamFinding{}, OrphanTeamGrants: []OrphanTeamGrant{}}

	// ① 全部团队映射（含禁用与软删豁免判定：软删行由 GORM 自动滤除——已删绑定不再点名，
	//    其遗留授权转由「悬空授权」段点名）
	teams, err := gorm.G[model.KBTeam](s.db).Order("id ASC").Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取团队映射失败: %w", err)
	}

	// ② 现行组名 → 最早命中 id（FindByName 口径：ORDER BY id 取首行）
	names := make([]string, 0, len(teams))
	for i := range teams {
		if teams[i].GroupName != "" {
			names = append(names, teams[i].GroupName)
		}
	}
	currentIDs := map[string]uint{}
	if len(names) > 0 {
		// 组名 → 现行组 id 经 user 轴主体面端口（未接入用户体系 ⇒ fail-closed 响亮报错，
		// 巡检不产出「全部团队都悬空」的假读数）
		ids, err := CurrentPrincipal().GroupNameIDs(ctx, names)
		if err != nil {
			return nil, fmt.Errorf("读取组名现行 id 失败: %w", err)
		}
		for name, id := range ids {
			currentIDs[name] = id
		}
	}

	// ③ 活跃团队授权的引用计数（GROUP BY subject_id；一次查询，非逐行）
	grantCounts := map[uint]int64{}
	{
		type teamCount struct {
			SubjectID uint  `gorm:"column:subject_id"`
			N         int64 `gorm:"column:n"`
		}
		rows := make([]teamCount, 0, len(teams))
		if err := gorm.G[model.KBGrant](s.db).
			Select("subject_id, COUNT(*) AS n").
			Where("subject_kind = ?", model.KBGrantSubjectTeam).
			Group("subject_id").
			Scan(ctx, &rows); err != nil {
			return nil, fmt.Errorf("统计团队授权失败: %w", err)
		}
		for i := range rows {
			grantCounts[rows[i].SubjectID] = rows[i].N
		}
	}

	liveTeamIDs := make([]uint, 0, len(teams))
	for i := range teams {
		team := teams[i]
		liveTeamIDs = append(liveTeamIDs, team.ID)

		finding := TeamFinding{
			TeamID:         team.ID,
			GroupName:      team.GroupName,
			DisplayName:    team.DisplayName,
			BoundGroupID:   team.BoundGroupID,
			CurrentGroupID: currentIDs[team.GroupName],
		}
		switch {
		case team.Status == nil || *team.Status != 1:
			finding.Kind = CheckKindDisabled
			finding.Hint = "团队映射已禁用：不参与读视图 teams(u)；授权行保留，恢复启用即生效"
		case finding.CurrentGroupID == 0:
			finding.Kind = CheckKindDangling
			finding.Hint = fmt.Sprintf("悬空：组名 %q 在 user_groups 已不存在（改名/删除）——相关授权对一切用户不可命中（fail-closed），待人工重绑", team.GroupName)
		case team.BoundGroupID == 0:
			finding.Kind = CheckKindOK
			finding.Hint = fmt.Sprintf("未记录绑定基线（bound_group_id=0）；现行最早命中组 id=%d——重绑一次即可启用回收检测", finding.CurrentGroupID)
		case finding.CurrentGroupID != team.BoundGroupID:
			finding.Kind = CheckKindRecycled
			finding.Hint = fmt.Sprintf("组名回收：绑定时 id=%d，现行最早命中 id=%d（改名后新建同名组会静默继承历史授权）——请确认后重绑", team.BoundGroupID, finding.CurrentGroupID)
		default:
			finding.Kind = CheckKindOK
			if n := grantCounts[team.ID]; n > 0 {
				finding.Hint = fmt.Sprintf("正常；本团队活跃授权 %d 行", n)
			} else {
				finding.Hint = "正常；无活跃授权"
			}
		}
		report.Teams = append(report.Teams, finding)
	}

	// ④ 悬空授权：team 授权 subject_id 不在现行团队 id 集内（含团队已软删/从未存在）
	orphans, err := s.orphanTeamGrants(ctx, liveTeamIDs)
	if err != nil {
		return nil, err
	}
	report.OrphanTeamGrants = orphans
	return report, nil
}

// orphanTeamGrants 悬空 team 授权（一次反查 + 内存差集）
func (s *Service) orphanTeamGrants(ctx context.Context, liveTeamIDs []uint) ([]OrphanTeamGrant, error) {
	type grantRow struct {
		ID        uint `gorm:"column:id"`
		LibraryID uint `gorm:"column:library_id"`
		SubjectID uint `gorm:"column:subject_id"`
	}
	q := gorm.G[model.KBGrant](s.db).
		Select("id, library_id, subject_id").
		Where("subject_kind = ?", model.KBGrantSubjectTeam)
	if len(liveTeamIDs) > 0 {
		q = q.Where("subject_id NOT IN ?", liveTeamIDs)
	}
	rows := make([]grantRow, 0, 8)
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("读取悬空团队授权失败: %w", err)
	}
	out := make([]OrphanTeamGrant, 0, len(rows))
	for i := range rows {
		out = append(out, OrphanTeamGrant{GrantID: rows[i].ID, LibraryID: rows[i].LibraryID, TeamID: rows[i].SubjectID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GrantID < out[j].GrantID })
	return out, nil
}
