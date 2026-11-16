// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"testing"
)

// TestSelectWaveCausalSupersedesCloseGuardAdversarial 守卫波次级关闭收窄契约：
// 启发式的「同关系异值」与「同客体异关系」会在多值关系（recommended/published/states…）上
// 误关并列事实；波次补关只保留 open 前瞻环兑现/取消，显式关闭目标由写侧 closes_prior_value
// 逐字命中，波次不得擅自扩大关闭面。
//
// 回归覆盖：
//   - 恢复 sameRelSlot 关闭（不看 loop_state）⇒ 用例 1 转红（published 并列被误关）；
//   - 恢复 sameSingleObj 关闭 ⇒ 用例 3 转红（同客体异关系被误关）；
//   - 去掉 open 环通道 ⇒ 用例 2 转红（合法兑现不再关停）。
func TestSelectWaveCausalSupersedesCloseGuardAdversarial(t *testing.T) {
	mk := func(node, subj, rel, obj uint64, relLabel, objLabel, loop string, order int) WaveCausalAssertion {
		return WaveCausalAssertion{
			NodeID: node, SubjectID: subj, RelID: rel, ObjectID: obj,
			RelLabel: relLabel, ObjectLabel: objLabel,
			World: "actual", Warrant: "referential", LoopState: loop,
			TurnHash: fmt.Sprintf("turn-%d", node), TSource: int64(100 + order), WaveOrder: order,
		}
	}

	// 用例 1（多值关系）：同一主体同一谓词、两个不同客体、都非 open ⇒ 不关停。
	rows := []WaveCausalAssertion{
		mk(1, 10, 20, 30, "published", "Book A", "", 0),
		mk(2, 10, 20, 31, "published", "Book B", "", 1),
	}
	if got := SelectWaveCausalSupersedes(rows, []uint64{2}); len(got) != 0 {
		t.Fatalf("多值关系被波次误关：%+v（期望 0 条）", got)
	}

	// 用例 2（open 前瞻环兑现）：旧行 open + 同关系谓词 ⇒ 允许关停（显式关闭目标外的唯一通道）。
	rows = []WaveCausalAssertion{
		mk(3, 10, 21, 40, "intends to run", "the test", LoopStateOpen, 0),
		mk(4, 10, 21, 41, "ran", "the test", "", 1),
	}
	got := SelectWaveCausalSupersedes(rows, []uint64{4})
	if len(got) != 1 || got[0].OlderNodeID != 3 || got[0].CloserNodeID != 4 {
		t.Fatalf("open 环兑现未关停：%+v（期望 3→4）", got)
	}

	// 用例 3（同客体异关系）：旧启发式 sameSingleObj 会关停；v18 必须不关。
	rows = []WaveCausalAssertion{
		mk(5, 10, 22, 50, "is a track on", "Theli", "", 0),
		mk(6, 10, 23, 50, "appears as track 7 on", "Theli", "", 1),
	}
	if got := SelectWaveCausalSupersedes(rows, []uint64{6}); len(got) != 0 {
		t.Fatalf("同客体异关系被波次误关：%+v（期望 0 条）", got)
	}
}
