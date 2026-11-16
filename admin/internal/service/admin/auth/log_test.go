package auth

import (
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/service"
)

// ==================== 日志默认时间窗 ====================

func TestHasCreatedAtFilter(t *testing.T) {
	cases := []struct {
		name   string
		wheres []service.WhereGroup
		want   bool
	}{
		{"无条件", nil, false},
		{"空条件组", []service.WhereGroup{{}}, false},
		{"其他字段", []service.WhereGroup{{Wheres: []service.Where{{Field: "admin_id", Operator: "eq", Value: uint(1)}}}}, false},
		{"created_at 精确匹配", []service.WhereGroup{{Wheres: []service.Where{{Field: "created_at", Operator: "egt"}}}}, true},
		{"created_at 前后带空格", []service.WhereGroup{{Wheres: []service.Where{{Field: "  created_at  ", Operator: "BETWEEN"}}}}, true},
		{"关联字段后缀", []service.WhereGroup{{Wheres: []service.Where{{Field: "admin.created_at", Operator: "egt"}}}}, true},
		{"多组命中其一", []service.WhereGroup{
			{Wheres: []service.Where{{Field: "username", Operator: "ILIKE"}}},
			{Wheres: []service.Where{{Field: "created_at", Operator: "BETWEEN"}}},
		}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasCreatedAtFilter(c.wheres); got != c.want {
				t.Fatalf("hasCreatedAtFilter() = %v，期望 %v", got, c.want)
			}
		})
	}
}

func TestAppendLogQueryWindow(t *testing.T) {
	// 未带 created_at 条件时补一组窗口条件，下界为「当前时间 - 默认窗口」并截断到整分钟
	t.Run("无过滤时补窗口", func(t *testing.T) {
		before := time.Now().Add(-adminLogListWindow)
		wheres := appendLogQueryWindow(nil)
		after := time.Now().Add(-adminLogListWindow)

		if len(wheres) != 1 || len(wheres[0].Wheres) != 1 {
			t.Fatalf("应补 1 组 1 条条件，实际 %+v", wheres)
		}
		w := wheres[0].Wheres[0]
		if w.Field != "created_at" || w.Operator != "egt" {
			t.Fatalf("条件应为 created_at egt，实际 %s %s", w.Field, w.Operator)
		}
		got, ok := w.Value.(time.Time)
		if !ok {
			t.Fatalf("条件值应为 time.Time，实际 %T", w.Value)
		}
		if got.Before(before.Add(-time.Minute)) || got.After(after) {
			t.Fatalf("窗口下界 %v 不在 [%v, %v] 内", got, before, after)
		}
		if got.Second() != 0 || got.Nanosecond() != 0 {
			t.Fatalf("窗口下界应截断到整分钟，实际 %v", got)
		}
	})

	// 已带 created_at 条件时原样返回，不叠加默认窗（否则查历史数据会被截断）
	t.Run("已有过滤时不叠加", func(t *testing.T) {
		wheres := []service.WhereGroup{{
			Wheres: []service.Where{{
				Field:    "created_at",
				Operator: "BETWEEN",
				Value:    "2026-01-01 00:00:00,2026-01-02 00:00:00",
			}},
		}}
		got := appendLogQueryWindow(wheres)
		if len(got) != 1 || len(got[0].Wheres) != 1 {
			t.Fatalf("不应追加条件，实际 %+v", got)
		}
		if got[0].Wheres[0].Operator != "BETWEEN" {
			t.Fatalf("原有条件被改写: %+v", got[0].Wheres[0])
		}
	})

	// 与运算符白名单的契约: egt 必须映射为 >=，否则窗口条件会被静默跳过（失效方向不安全）
	t.Run("egt 在运算符白名单内", func(t *testing.T) {
		if op := service.GetOperatorByAlias("egt"); op != ">=" {
			t.Fatalf("egt 应映射为 >=，实际 %q——白名单若被改，默认时间窗会被静默丢弃", op)
		}
	})
}
