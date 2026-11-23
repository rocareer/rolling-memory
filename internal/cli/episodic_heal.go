// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

const (
	// episodicHealTickLimit 是常规后台 tick 每次巡检的指针行上限：游标跨 tick 前进，
	// 全库被逐步扫完一轮后自动回卷，任何单 tick 都不会无界。
	episodicHealTickLimit = 128
	// episodicHealStepLimit 是控制台「推进一轮」这类显式 step 每次巡检的页大小。
	episodicHealStepLimit = 512
)

// episodicHealCursorKey 返回按 scope 隔离的巡检游标键：多个 scope 各自独立推进。
func episodicHealCursorKey(scope string) string {
	return "episodic_heal_cursor:" + scope
}

// sameEpisodicSourceSession 判断两个情景指针是否属于同一个源：jsonl 指针的源身份是
// 文件路径（轮次序号可在再导入间漂移），zcode 指针的源身份是会话 ID（消息 ID 与会话内
// 编号稳定）。轮次与段号不同不影响判定，源不同则一律为 false。
func sameEpisodicSourceSession(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case strings.HasPrefix(a, "@ref:jsonl:") && strings.HasPrefix(b, "@ref:jsonl:"):
		fileA, fileB := jsonlPointerSourceFile(a), jsonlPointerSourceFile(b)
		return fileA != "" && fileA == fileB
	case strings.HasPrefix(a, "@ref:zcode:") && strings.HasPrefix(b, "@ref:zcode:"):
		sessionA, sessionB := zcodePointerSession(a), zcodePointerSession(b)
		return sessionA != "" && sessionA == sessionB
	case strings.HasPrefix(a, "@ref:opencode:") && strings.HasPrefix(b, "@ref:opencode:"):
		sessionA, sessionB := opencodePointerSession(a), opencodePointerSession(b)
		return sessionA != "" && sessionA == sessionB
	default:
		return false
	}
}

// jsonlPointerSourceFile 提取 `@ref:jsonl:<file>:<turn>#<seg>` 的文件部分。
func jsonlPointerSourceFile(pointer string) string {
	rest := strings.TrimPrefix(strings.TrimSpace(pointer), "@ref:jsonl:")
	if idx := strings.LastIndex(rest, "#"); idx > 0 {
		rest = rest[:idx]
	}
	if idx := strings.LastIndex(rest, ":"); idx > 0 {
		rest = rest[:idx]
	}
	return rest
}

// zcodePointerSession 提取 `@ref:zcode:<session>:<message>#<seg>` 的会话部分。
func zcodePointerSession(pointer string) string {
	rest := strings.TrimPrefix(strings.TrimSpace(pointer), "@ref:zcode:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[0]
}

// opencodePointerSession 提取 `@ref:opencode:<session>:<message>#<seg>` 的会话部分。
func opencodePointerSession(pointer string) string {
	rest := strings.TrimPrefix(strings.TrimSpace(pointer), "@ref:opencode:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[0]
}

// healDriftedEpisodicPointers 在常规 tick 上做「只愈合、不回收」的情景指针巡检：
// 按游标取一页指针行，逐行分类；封条内容漂移到其他坐标的行重指并重封（含跨轮次重锚）。
// 确证不存在的行不在此处回收——回收永远留在 `sync prune --apply --yes`；
// 源不可读的行原样保留。返回 (扫描的指针行数, 实际修复的行数)。
func healDriftedEpisodicPointers(ctx context.Context, memory *facade.Memory, limit int) (int, int, error) {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return 0, 0, errors.New("episodic heal requires an open local store")
	}
	if limit <= 0 || limit > episodicPruneScanLimit {
		limit = episodicHealTickLimit
	}
	scope := config.DefaultScope
	if memory.Config != nil && strings.TrimSpace(memory.Config.Scope) != "" {
		scope = strings.TrimSpace(memory.Config.Scope)
	}
	tc, err := memory.LocalVault()
	if err != nil {
		return 0, 0, err
	}
	if tc == nil || tc.Vault == nil {
		return 0, 0, errors.New("episodic heal requires a local vault")
	}
	db := memory.Store.DB()

	cursorKey := episodicHealCursorKey(scope)
	var cursorRaw []byte
	if err := db.QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, cursorKey).Scan(&cursorRaw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	cursor := string(cursorRaw)

	var rows []episodicHealRow
	result, err := db.QueryContext(ctx, `
		SELECT d.unit_id, COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text), d.occurred_at
		FROM rm_docs d
		LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id
		WHERE (d.layer = 'episodic' OR d.layer = '')
		  AND d.unit_id > ?
		ORDER BY d.unit_id
		LIMIT ?`, cursor, limit)
	if err != nil {
		return 0, 0, err
	}
	for result.Next() {
		var row episodicHealRow
		if err := result.Scan(&row.docID, &row.refPointer, &row.occurredAt); err != nil {
			result.Close()
			return 0, 0, err
		}
		rows = append(rows, row)
	}
	if err := result.Err(); err != nil {
		result.Close()
		return 0, 0, err
	}
	result.Close()

	scanned, healed, processed := 0, 0, 0
	for index, row := range rows {
		if ctx.Err() != nil {
			break
		}
		refPointer := strings.TrimSpace(row.refPointer)
		if !strings.HasPrefix(refPointer, "@ref:") {
			// 内联单元没有坐标可漂移，常规巡检不为其付分类成本。
			processed = index + 1
			continue
		}
		if facade.ShouldYieldToForegroundRecall() {
			break
		}
		scanned++
		// 只读分类：原地补索引由同一 tick 的回填负责（heal=true 的 4.4ms/行 → 0.8ms/行），
		// 巡检只为「漂移」这一件事写库，且每次都走下面带双证的同源重锚路径。
		status := tc.Vault.ClassifyAndHealEpisodicUnitContext(ctx, scope, row.docID, refPointer, row.occurredAt, false)
		if ctx.Err() != nil {
			break
		}
		if status != localvault.EpisodicContentVerified {
			if status == localvault.EpisodicContentRelocatable {
				// 只做同源重锚：备用坐标必须落在与行内指针同一个源（jsonl = 同一文件、
				// zcode = 同一会话）。跨源备用内容属于交付侧明确拒绝的「未归属替代」，
				// 自动巡检不得软化该裁定——这类行保持来源缺口，留待人工处理。
				if _, actualRef, rErr := tc.Vault.LoadVerifiedEpisodicPointerSource(row.docID, refPointer); rErr == nil && sameEpisodicSourceSession(refPointer, actualRef) {
					if newRef, reErr := tc.Vault.ReanchorRelocatedEpisodicUnit(scope, row.docID, refPointer, row.occurredAt, true); reErr == nil && newRef != "" {
						healed++
						processed = index + 1
						continue
					}
				}
			}
			// 同源按封条摘要重扫：序号位移会让行被判 absent/unreadable，但内容可能仍活在
			// 同一文件里——按摘要找回真实坐标并重锚（只限同文件，跨会话替代永不发生，且从不回收）。
			if candidate, rErr := tc.Vault.ReanchorSameSourceByDigest(scope, row.docID, refPointer, row.occurredAt, true); rErr == nil && candidate != "" {
				healed++
			}
		}
		processed = index + 1
	}
	// 游标只跨越本轮真正处理完的行；yield 或取消提前退出时，本页剩余行
	// 留给下一轮，而不是被记成已巡检。
	nextCursor := episodicHealNextCursor(cursor, rows, processed, limit)
	if _, err := db.Exec(`INSERT OR REPLACE INTO rm_meta(k, v) VALUES (?, ?)`, cursorKey, []byte(nextCursor)); err != nil {
		return 0, 0, err
	}
	return scanned, healed, nil
}

// episodicHealRow is one episodic pointer row fetched for a heal pass.
type episodicHealRow struct {
	docID      string
	refPointer string
	occurredAt int64
}

// episodicHealNextCursor advances the scan cursor only across rows this pass
// completed. A yield or cancellation therefore leaves the rest of the page for
// the next pass instead of recording it as inspected.
func episodicHealNextCursor(cursor string, rows []episodicHealRow, processed, limit int) string {
	if processed < len(rows) {
		if processed == 0 {
			return cursor
		}
		return rows[processed-1].docID
	}
	if limit > 0 && len(rows) == limit {
		return rows[len(rows)-1].docID
	}
	return ""
}
