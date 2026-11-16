// plugin: kb v1.0.0
package tag

import (
	"context"
	"strings"
	"testing"
)

// ==================== 候选输出解析（纯净函数；无库） ====================

// TestParseCandidatesUnit 候选输出契约：JSON 数组解析 / 围栏与前后缀容错 / 坏元素单丢 /
// 去重与置信度裁决 / 确定性排序 / 截断 / 非数组响亮失败（不把「模型没按契约输出」当空结果吞掉）。
func TestParseCandidatesUnit(t *testing.T) {
	// ① 纯 JSON 数组：置信度降序 → 名升序（确定性）
	out, err := ParseCandidates(`[{"tag":"合同","confidence":0.4},{"tag":"价款","confidence":0.9}]`, 5)
	if err != nil {
		t.Fatalf("纯数组解析失败: %v", err)
	}
	if len(out) != 2 || out[0].Tag != "价款" || out[1].Tag != "合同" {
		t.Fatalf("排序/解析不符: %+v", out)
	}

	// ② ``` 围栏 + 前后多余文字：取首 '[' 与末 ']' 之间切片
	fenced := "```json\n[{\"tag\":\"知识库\",\"confidence\":0.8}]\n```"
	out, err = ParseCandidates(fenced, 5)
	if err != nil || len(out) != 1 || out[0].Tag != "知识库" {
		t.Fatalf("围栏输出解析失败: %+v err=%v", out, err)
	}

	// ③ 坏元素单丢（空名/超长/名合法者保留）；confidence 越界钳位
	long := strings.Repeat("长", maxTagRunes+1)
	out, err = ParseCandidates(`[{"tag":"","confidence":0.5},{"tag":"`+long+`","confidence":0.5},`+
		`{"tag":"有效","confidence":3},{"tag":"负值","confidence":-1}]`, 5)
	if err != nil {
		t.Fatalf("含坏元素应只丢元素不失败: %v", err)
	}
	byName := map[string]float64{}
	for _, c := range out {
		byName[c.Tag] = c.Confidence
	}
	if _, ok := byName["有效"]; !ok || byName["有效"] != 1 {
		t.Fatalf("confidence>1 应钳位 1: %+v", out)
	}
	if _, ok := byName["负值"]; !ok || byName["负值"] != 0 {
		t.Fatalf("confidence<0 应钳位 0: %+v", out)
	}
	if len(out) != 2 {
		t.Fatalf("坏元素应被丢弃（保留 2 条）: %+v", out)
	}

	// ④ 去重（归一化：大小写/首尾空白）保留最高置信度；截断 max
	out, err = ParseCandidates(`[{"tag":"Tag","confidence":0.2},{"tag":" tag ","confidence":0.7},`+
		`{"tag":"B","confidence":0.9},{"tag":"C","confidence":0.1}]`, 2)
	if err != nil {
		t.Fatalf("去重解析失败: %v", err)
	}
	if len(out) != 2 || out[0].Tag != "B" {
		t.Fatalf("截断/排序不符: %+v", out)
	}
	found := false
	for _, c := range out {
		if strings.EqualFold(strings.TrimSpace(c.Tag), "tag") {
			found = true
			if c.Confidence != 0.7 {
				t.Fatalf("去重应保留最高置信度: %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("去重后应保留 Tag 一条: %+v", out)
	}

	// ⑤ 空数组 = 合法（宁缺勿滥）
	out, err = ParseCandidates("[]", 5)
	if err != nil || len(out) != 0 {
		t.Fatalf("空数组应合法空集: %+v err=%v", out, err)
	}

	// ⑥ 非数组输出 = 响亮失败（不得静默吞成空结果）
	if _, err := ParseCandidates("抱歉，我无法从这段材料抽取标签。", 5); err == nil {
		t.Fatal("非数组输出应报错")
	}
	if _, err := ParseCandidates("[not json", 5); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

// TestMatchCandidatesUnit 候选 ↔ 词表比对：精确命中（归一化比较）/ 未入词表只回显 / 词表空缺不命中。
func TestMatchCandidatesUnit(t *testing.T) {
	vocab := []TagRef{
		{TagID: 11, Name: "合同", GroupID: 1, GroupName: "业务", Scope: "platform"},
		{TagID: 12, Name: "价款", GroupID: 1, GroupName: "业务", Scope: "platform"},
	}
	cands := []Candidate{{Tag: "合同", Confidence: 0.9}, {Tag: " 价款 ", Confidence: 0.5}, {Tag: "未入词表", Confidence: 0.3}}
	rows := MatchCandidates(cands, vocab)
	if len(rows) != 3 {
		t.Fatalf("比对行数不符: %+v", rows)
	}
	if !rows[0].InVocabulary || rows[0].TagID != 11 {
		t.Fatalf("命中项应带词表 id: %+v", rows[0])
	}
	if !rows[1].InVocabulary || rows[1].TagID != 12 {
		t.Fatalf("归一化后应命中（首尾空白忽略）: %+v", rows[1])
	}
	if rows[2].InVocabulary || rows[2].TagID != 0 {
		t.Fatalf("未入词表项不得带 id: %+v", rows[2])
	}
	if n := countUnmatched(rows); n != 1 {
		t.Fatalf("未入词表计数不符: %d", n)
	}
	// 空词表：全部未命中（但保留回显，绝不落库）
	rows = MatchCandidates(cands, nil)
	if len(rows) != 3 || countUnmatched(rows) != 3 {
		t.Fatalf("空词表应全未命中: %+v", rows)
	}
}

// TestRunCandidatesDisabledUnit 缺省关档（无库依赖）：不调模型、不产候选、不触任何表（db 为 nil 也不 panic）。
func TestRunCandidatesDisabledUnit(t *testing.T) {
	svc := &Service{enabled: func() bool { return false }}
	res, err := svc.RunCandidates(context.Background(), 12345)
	if err != nil {
		t.Fatalf("关档不应报错: %v", err)
	}
	if res == nil || res.State != "disabled" || len(res.Candidates) != 0 {
		t.Fatalf("关档回执不符: %+v", res)
	}
	// document_id 缺失（0）：响亮失败（入参校验先于开关）
	if _, err := svc.RunCandidates(context.Background(), 0); err == nil {
		t.Fatal("缺 document_id 应报错")
	}
}
