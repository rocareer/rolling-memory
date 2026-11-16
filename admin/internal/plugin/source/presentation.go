package source

import "fmt"

// EntryFlag 清单行的跨来源标记（管理面与 CLI 共用同一实现，避免复制件漂移）。
type EntryFlag struct {
	SameContent    bool `json:"same_content"`
	SourceConflict bool `json:"source_conflict"`
}

// EntryRowKey 行键 = source_id + uid + version（跨来源同 uid 不能用 uid/source_id 单独做键）。
func EntryRowKey(row CatalogEntry) string {
	return fmt.Sprintf("%d|%s|%s", row.SourceID, row.UID, row.Version)
}

// EntryPeerFlags 以 (uid, version) 分组计算 same_content/source_conflict；
// peers 必须是全来源的相关行（调用方不能只传当前页）。
func EntryPeerFlags(rows, peers []CatalogEntry) map[string]EntryFlag {
	byKey := make(map[string][]CatalogEntry)
	for i := range peers {
		key := peers[i].UID + "@" + peers[i].Version
		byKey[key] = append(byKey[key], peers[i])
	}
	out := make(map[string]EntryFlag, len(rows))
	for i := range rows {
		key := rows[i].UID + "@" + rows[i].Version
		group := byKey[key]
		flag := EntryFlag{SameContent: len(group) > 1}
		for _, peer := range group {
			if !equalFold(peer.PackageSHA256, rows[i].PackageSHA256) {
				flag.SourceConflict = true
				flag.SameContent = false
			}
		}
		out[EntryRowKey(rows[i])] = flag
	}
	return out
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
