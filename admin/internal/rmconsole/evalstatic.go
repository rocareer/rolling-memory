// plugin: rolling-memory v0.0.58
// Package rollingmemory evalstatic.go —— 基线与榜单台账的唯一数据装载点。
//
// 封印基线（referenceBaselines / atmReferenceBaselines）与 ATM 公开榜单
// （buildATMBenchLeaderboard）是纯数据，正本在 evalstatic/*.json（go:embed 随
// 二进制分发）：内核提分刷新读数只改 JSON 数据文件，不再做 Go 源码手术。
// 装载失败即 panic fail-fast——嵌入数据损坏属于构建产物级缺陷，禁止静默空台账上屏。
package rollingmemory

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed evalstatic/locomo_baselines.json evalstatic/atm_baselines.json evalstatic/atm_leaderboard.json
var evalStaticFS embed.FS

var evalStaticOnce struct {
	sync.Once
	locomoBaselines []EvalRunRecord
	atmBaselines    []EvalRunRecord
	atmBoard        ATMBenchBoard
}

func loadEvalStatic() {
	evalStaticOnce.Do(func() {
		mustLoad := func(name string, v any) {
			raw, err := evalStaticFS.ReadFile("evalstatic/" + name)
			if err != nil {
				panic(fmt.Sprintf("evalstatic: embedded %s unreadable: %v", name, err))
			}
			if err := json.Unmarshal(raw, v); err != nil {
				panic(fmt.Sprintf("evalstatic: embedded %s corrupt: %v", name, err))
			}
		}
		mustLoad("locomo_baselines.json", &evalStaticOnce.locomoBaselines)
		mustLoad("atm_baselines.json", &evalStaticOnce.atmBaselines)
		mustLoad("atm_leaderboard.json", &evalStaticOnce.atmBoard)
	})
}

// referenceBaselines 返回 LoCoMo 封印基线台账（只读深拷贝切片头，元素共享不可变数据）。
func referenceBaselines() []EvalRunRecord {
	loadEvalStatic()
	out := make([]EvalRunRecord, len(evalStaticOnce.locomoBaselines))
	copy(out, evalStaticOnce.locomoBaselines)
	return out
}

// atmReferenceBaselines 返回 ATM-Bench 封印基线台账。
func atmReferenceBaselines() []EvalRunRecord {
	loadEvalStatic()
	out := make([]EvalRunRecord, len(evalStaticOnce.atmBaselines))
	copy(out, evalStaticOnce.atmBaselines)
	return out
}

// buildATMBenchLeaderboard 返回 ATM-Bench 公开榜单（值拷贝，Entries 切片共享只读底层）。
func buildATMBenchLeaderboard() ATMBenchBoard {
	loadEvalStatic()
	return evalStaticOnce.atmBoard
}
