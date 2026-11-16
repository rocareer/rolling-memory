// plugin: kb v1.0.0
// 查询向量腿的嵌入面（「请求路径零内联嵌入」的边界澄清 = 请求路径不发**切片内联嵌入**；
// 查询嵌入本身经 aiport 端口 + 进程内查询向量缓存，同一查询不重复计费）。
//
// 契约（与 embed 包同源）：
//   - 调用档名 = kb-embed（经 CallMeta.Agent 透传；产品适配器映射到其 ai agent，模型口径归其运行时）；
//   - **向量身份 = 嵌入 Provider 的模型标识**：查询向量缓存键与向量腿
//     `v.model` 过滤共用单点（embed.ModelIdentity() → Provider.Identity()）⇒ 换模型：
//     缓存键换、过滤面换，旧身份向量行自然不召回、旧缓存不复活；
//   - Source = "kb" ⇒ 批处理优先级档与调用日志由产品适配器映射；
//   - 嵌入文本 = 查询原文**逐字**（与切片侧 ChunkTextOf 派生的向量空间对齐：查询不加任何前缀）；
//   - 返回向量经 pgvector 文本形态（[a,b,c]，float32 位宽）内联进 SQL 参数（值参数化，无注入面）。
package search

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/aiport"
	kbembed "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/embed"
)

// QueryAgent 查询嵌入调用档名（与切片嵌入同档——向量空间必须同一；产品适配器映射到其 ai agent）。
// 身份口径：与切片侧同档（embed.DefaultAgent 同值，改写须两处同改）；**身份解析单点**走
// kbembed.ModelIdentity()。
// 历史注（已整改）：键内身份原取 agent 名（非模型标识）⇒ 后台改
// kb-embed 的 model 时不换行/不换缓存键、同库混用新旧向量空间；现缓存键与检索腿过滤均按模型身份。
const QueryAgent = "kb-embed"

// Source 调用来源（批处理优先级档）
const Source = "kb"

// logWarnOnce 落盘失败告警节流（进程内一次；语义：不刷屏，不阻断）
var logWarnOnce sync.Once

// resolvedModelIdentity 当前模型身份（检索腿 `v.model` 过滤与查询向量缓存键共用单点）。
// "" ⇒ 返回错误（调用方把腿按失败处理：hybrid 降该腿进 degraded[]、vector 单腿 = 整查失败）——
// 禁按空身份发查询（等值空串会让无身份行混入，机械短路归仓储层 HasVectorContent/SearchVector）。
//
// 已知窗口（如实登记）：本函数与 queryVector 内的身份解析是两次相邻只读（同一函数链内、无 I/O 间隔），
// 仅当 admin 恰在此亚毫秒窗口内改 model 定义才可能一次查询过滤面与缓存键面错位（自愈：下一查即修正；
// 行集仍按可读集下推，不涉越权）。调用级漂移（解析→调用之间）由 queryVector 的回显核对拦住。
func resolvedModelIdentity() (string, error) {
	identity := kbembed.ModelIdentity()
	if identity == "" {
		return "", errors.New("嵌入模型身份解析失败（Provider 未注册或模型未配置）")
	}
	return identity, nil
}

// queryVector 查询向量（缓存命中 ⇒ 免重嵌；未命中 ⇒ 嵌入后入缓存；落盘失败不阻断）。
// 缓存键 = **模型身份 + query**（非 agent 名；换模型 ⇒ 换键，旧空间向量不复用）。
// 调用后核对回显身份（res.AgentModel = agent 定义的路由模型名）：与本进程解析身份漂移 ⇒ 响亮失败
// （admin 改 model 与本次调用并发的护栏；漂移结果不入缓存、不参与检索）。
func (s *Service) queryVector(ctx context.Context, query string) ([]float32, error) {
	identity, err := resolvedModelIdentity()
	if err != nil {
		return nil, err
	}
	if s.qvec != nil {
		if v, ok := s.qvec.Get(identity, query); ok && len(v) > 0 {
			return v, nil
		}
	}
	res, err := aiportEmbed(ctx, query)
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Vectors) != 1 || len(res.Vectors[0]) == 0 {
		return nil, fmt.Errorf("查询嵌入返回非法（向量条数不符或空向量）")
	}
	if got := strings.TrimSpace(res.ModelIdentity); got != identity {
		return nil, fmt.Errorf("查询嵌入模型身份漂移（解析身份=%s 调用回显=%s）：拒绝按跨空间向量检索", identity, got)
	}
	vec := res.Vectors[0]
	if s.qvec != nil {
		if perr := s.qvec.Put(identity, query, vec); perr != nil {
			logWarnOnce.Do(func() {
				log.Printf("[kb] 查询向量缓存落盘失败（内存仍命中，不阻断检索）: %v", perr)
			})
		}
	}
	return vec, nil
}

// aiportEmbed 查询向量经端口生成（Face 为空 = 不做出站声明——与「查询嵌入不声明 egress」的既有口径一致；
// 档名经 CallMeta.Agent 透传，产品适配器映射到其 ai agent 与批处理档）。
func aiportEmbed(ctx context.Context, query string) (*aiport.EmbedResult, error) {
	provider, err := aiport.CurrentEmbedder()
	if err != nil {
		return nil, err
	}
	res, err := provider.Embed(ctx, aiport.CallMeta{Kind: "query", Agent: QueryAgent, Source: Source}, []string{query})
	if err != nil {
		return nil, fmt.Errorf("查询嵌入失败（档 %s）: %w", QueryAgent, err)
	}
	return res, nil
}

// formatEmbedding float32 向量 → pgvector 文本形态 [a,b,c]（'f' 格式、-1 精度、float32 位宽——
// 格式与 embed 包的向量文本形态逐字一致，两处各自持有以避跨模块耦合）。
func formatEmbedding(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
