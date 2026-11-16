package database

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
	"gorm.io/plugin/dbresolver"
	_ "modernc.org/sqlite"
)

var db *gorm.DB

// IsSQLite 判断当前数据库是否为 SQLite。
func IsSQLite() bool {
	if db != nil && db.Dialector != nil {
		return db.Dialector.Name() == "sqlite"
	}
	return strings.EqualFold(config.Get().Database.Type, "sqlite")
}

// 初始化数据库连接
func Init() error {
	cfg := config.Get().Database

	newLogger := logger.New(
		log.New(os.Stderr, "", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  gin.Mode() == gin.DebugMode,
		},
	)

	gormCfg := &gorm.Config{Logger: newLogger}
	if cfg.Prefix != "" {
		gormCfg.NamingStrategy = schema.NamingStrategy{
			TablePrefix: cfg.Prefix,
		}
	}

	var err error
	if strings.EqualFold(cfg.Type, "sqlite") || cfg.Type == "" {
		dbPath := strings.TrimSpace(cfg.Write.DBName)
		if dbPath == "" || dbPath == "aigo" || dbPath == "data/rolling-admin.db" {
			home, _ := os.UserHomeDir()
			dbPath = filepath.Join(home, ".local/share/rolling-memory/admin/rolling-admin.db")
		} else if strings.HasPrefix(dbPath, "~/") {
			home, _ := os.UserHomeDir()
			dbPath = filepath.Join(home, strings.TrimPrefix(dbPath, "~/"))
		}
		if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				return fmt.Errorf("create sqlite dir %s: %w", dir, mkErr)
			}
		}
		dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		db, err = gorm.Open(sqlite.New(sqlite.Config{
			DriverName: "sqlite",
			DSN:        dsn,
		}), gormCfg)
		if err != nil {
			return fmt.Errorf("connect sqlite database (%s): %w", dbPath, err)
		}
		sqlDB, sqlErr := db.DB()
		if sqlErr != nil {
			return fmt.Errorf("get sqlite sql.DB: %w", sqlErr)
		}
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		if bootErr := bootstrapSQLite(db); bootErr != nil {
			return fmt.Errorf("bootstrap sqlite schema: %w", bootErr)
		}
		return nil
	}

	db, err = gorm.Open(postgres.Open(buildDSN(cfg.Write)), gormCfg)
	if err != nil {
		return fmt.Errorf("connect write database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB: %w", err)
	}

	// 连接池配置
	sqlDB.SetMaxOpenConns(cfg.Write.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.Write.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.Write.ConnMaxLifetime) * time.Second)

	// 配置读写分离
	if cfg.Read.Enabled {
		resolver := dbresolver.Register(dbresolver.Config{
			Replicas: []gorm.Dialector{postgres.Open(buildDSN(cfg.Read.DatabaseSource))},
		})
		resolver.SetMaxOpenConns(cfg.Read.MaxOpenConns)
		resolver.SetMaxIdleConns(cfg.Read.MaxIdleConns)
		resolver.SetConnMaxLifetime(time.Duration(cfg.Read.ConnMaxLifetime) * time.Second)

		if err := db.Use(resolver); err != nil {
			return fmt.Errorf("configure read database: %w", err)
		}
	}

	return nil
}

// 返回全局数据库实例
func DB() *gorm.DB {
	return db
}

// 将 *gorm.DB 注入 gin.Context，绑定当前请求的 context
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("db", DB().WithContext(c.Request.Context()))
		c.Next()
	}
}

// 从 gin.Context 中获取带请求上下文的 *gorm.DB
func FromContext(c *gin.Context) *gorm.DB {
	return c.MustGet("db").(*gorm.DB)
}

// 根据配置构建 PostgreSQL DSN
func buildDSN(src config.DatabaseSource) string {
	password := ""
	if src.Password != "" {
		escaped := strings.ReplaceAll(src.Password, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "'", "\\'")
		password = fmt.Sprintf(" password='%s'", escaped)
	}

	return fmt.Sprintf("host=%s user=%s%s dbname=%s port=%d sslmode=%s TimeZone=%s",
		src.Host, src.User, password, src.DBName, src.Port, src.SSLMode, config.Get().App.Timezone,
	)
}
