// plugin: ai v0.2.0
// domain_closure.go 是调用类型闭集的 **TTS 增量**：plugin_ai_model.type 与
// plugin_ai_usage.call_type 的闭集从 chat|embedding 扩为 chat|embedding|tts。
//
// 归属（单一事实源，刻意不搬常量）：chat / embedding 的取值沿用既有的两个定义点——
//   - ModelTypeChat / ModelTypeEmbedding：model.go（plugin_ai_model.type）；
//   - CallTypeChat / CallTypeEmbedding：router.go（plugin_ai_model.type 与 plugin_ai_usage.call_type 共用）。
//
// 本文件只补 TTS 这两个增量常量，同一取值不在两处重复定义。
//
// **与 DB CHECK 的时序纪律（硬约束，feedback 1）**：Go 侧闭集一旦放宽，落 type='tts' /
// call_type='tts' 的行会撞 migrations/1_init.up.sql:111 与 :200 的旧 CHECK（chat|embedding），
// 要么被 PostgreSQL 拒（整行 22014）、要么在 handler 侧以 500 收场。DB 侧 CHECK 由后续迁移
// （迁移 6：DROP 旧约束 + ADD 新约束）落地，故：
//   - 本二进制落地后、迁移 6 落地前，不得对任何库 install，也不得把本二进制指向未跑迁移 6 的库；
//     该窗口内的 v1 基线库快照与试跑一律用早前的二进制；
//   - 本包的断言**只在接口层**（normalizeModelType / usage-log 闭集校验这类纯函数），
//     不主张任何真库断言——真库落 type='tts' 由迁移 6 + voice_db_test 覆盖。
//
// 源侧对照（只读）：admin/model/AiModel.php:21（TYPE_TTS = 'tts'）与
// admin/controller/ai/Model.php:234（type ∈ chat|embedding|tts，否则回落 chat）。
// 源侧 ai_usage.call_type 还含 'asr'（20260909155604_radmin_ai_voice.php:34），
// 但 v1 没有语音转写入口 ⇒ 本批闭集**不含 asr**（value 被拒是刻意口径，见 handler_usage.go）。
package ai

// 调用类型闭集的 TTS 增量（chat|embedding 沿用 model.go / router.go）。
const (
	// ModelTypeTTS 是 plugin_ai_model.type 的语音合成取值（源 AiModel::TYPE_TTS）。
	ModelTypeTTS = "tts"
	// CallTypeTTS 是 plugin_ai_usage.call_type 的语音合成取值（源迁移 ADD VALUE 'tts'）。
	CallTypeTTS = "tts"
)
