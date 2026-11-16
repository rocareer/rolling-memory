package host

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// defaultGateTTL 运行时闸的状态缓存上限（按请求读库 + ≤1s 缓存 ⇒ CLI 启停 ≤1s 生效）。
// /plugin.gate_ttl_seconds 配置项未落宿主 config（改动面总表不含
// internal/infra/config/config.go）⇒ 此处以进程内默认值 + SetGateTTL 扩展点替代：
// 接线侧（internal/plugins/wiring.go）或下游项目如需调整缓存上限，在启动时调用 SetGateTTL 即可。
const defaultGateTTL = time.Second

// gate 进程内唯一的运行时闸状态
var gate = newGate(defaultGateTTL, readStatus)

// SetGateTTL 设置闸状态缓存上限（<=0 恢复默认）；变更会清空已缓存状态。
func SetGateTTL(ttl time.Duration) {
	gate.setTTL(ttl)
}

// Gate 返回插件运行时闸：按请求读 admin_plugins.status（≤TTL 缓存），
// 非 enabled 一律 404——不承认插件存在性、不进业务 handler。
// 查库失败按未启用处理（fail-closed 且只记日志）：闸是放行条件，拿不到状态就不放行。
func Gate(name string) gin.HandlerFunc {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("host: Gate 需要非空插件名")
	}
	return func(c *gin.Context) {
		if !gate.enabled(c.Request.Context(), name) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
}

// readStatus 按名读插件状态（按请求读库）；台账无此行按空状态处理（由闸判为未启用），
// 不把「未安装」混进错误日志。
func readStatus(ctx context.Context, name string) (string, error) {
	db := database.DB()
	if db == nil {
		return "", errors.New("插件闸: 数据库未初始化")
	}
	row, err := gorm.G[Plugin](db).Select("status").Where("name = ?", name).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.Status, nil
}

// gateState 闸状态缓存：按插件名缓存 status，TTL 内不重复查库
type gateState struct {
	mu   sync.Mutex
	ttl  time.Duration
	rows map[string]gateRow
	read func(ctx context.Context, name string) (string, error)
}

// gateRow 一条缓存状态
type gateRow struct {
	status  string
	expires time.Time
}

// newGate 构造闸状态缓存（read 为状态读取函数，测试可注入假实现）
func newGate(ttl time.Duration, read func(ctx context.Context, name string) (string, error)) *gateState {
	return &gateState{ttl: ttl, rows: map[string]gateRow{}, read: read}
}

// setTTL 更新缓存上限并清空旧缓存
func (g *gateState) setTTL(ttl time.Duration) {
	if ttl <= 0 {
		ttl = defaultGateTTL
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ttl = ttl
	g.rows = map[string]gateRow{}
}

// enabled 判定插件是否启用（查库失败按未启用处理，且不写入缓存）
func (g *gateState) enabled(ctx context.Context, name string) bool {
	status, err := g.status(ctx, name)
	if err != nil {
		log.Printf("[plugin] 读取插件 %s 状态失败（按未启用处理）: %v", name, err)
		return false
	}
	return status == StatusEnabled
}

// status 取插件状态（命中未过期缓存即返回；缓存起点取读取前，保证失效延迟不超过 TTL）
func (g *gateState) status(ctx context.Context, name string) (string, error) {
	now := time.Now()

	g.mu.Lock()
	row, cached := g.rows[name]
	ttl := g.ttl
	g.mu.Unlock()
	if cached && now.Before(row.expires) {
		return row.status, nil
	}

	status, err := g.read(ctx, name)
	if err != nil {
		return "", err
	}

	g.mu.Lock()
	g.rows[name] = gateRow{status: status, expires: now.Add(ttl)}
	g.mu.Unlock()
	return status, nil
}
