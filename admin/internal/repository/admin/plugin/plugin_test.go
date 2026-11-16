package plugin

import "testing"

// 纯单元测试（不连库、默认跑）：钉住审计分页的收敛口径——
// SQL 纪律要求「列表必须分页且有上限」，收敛单点在本包（NormalizeAuditPage），服务层同用它回显生效值。

func TestNormalizeAuditPage(t *testing.T) {
	cases := []struct {
		name                string
		page, limit         int
		wantPage, wantLimit int
	}{
		{name: "正常值原样返回", page: 3, limit: 50, wantPage: 3, wantLimit: 50},
		{name: "页码下界收敛到 1", page: 0, limit: 20, wantPage: 1, wantLimit: 20},
		{name: "负页码收敛到 1", page: -5, limit: 20, wantPage: 1, wantLimit: 20},
		{name: "条数下界回落默认值", page: 1, limit: 0, wantPage: 1, wantLimit: auditDefaultLimit},
		{name: "条数上界收敛到硬上限", page: 1, limit: 100000, wantPage: 1, wantLimit: auditMaxLimit},
		{name: "上界恰为硬上限", page: 2, limit: auditMaxLimit, wantPage: 2, wantLimit: auditMaxLimit},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, limit := NormalizeAuditPage(tc.page, tc.limit)
			if page != tc.wantPage || limit != tc.wantLimit {
				t.Fatalf("NormalizeAuditPage(%d, %d) = (%d, %d)，期望 (%d, %d)",
					tc.page, tc.limit, page, limit, tc.wantPage, tc.wantLimit)
			}
		})
	}
}
