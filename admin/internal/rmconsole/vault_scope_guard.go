package rollingmemory

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/gin-gonic/gin"
	"github.com/pelletier/go-toml/v2"
)

// consoleVaultScopeGuard protects direct console access to the single-scope vault.
// The console currently labels its local data as UserScope, so a different
// configured scope is rejected rather than attributing data to the wrong scope.
func consoleVaultScopeGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only explicitly identified non-vault endpoints bypass this guard. New
		// routes stay protected until their complete data path has been reviewed.
		path := c.FullPath()
		if strings.Contains(path, "/eval/") || strings.Contains(path, "/cloud/account/") ||
			path == "/admin/plugin/rolling-memory/ai/config" || path == "/admin/plugin/rolling-memory/ai/save" ||
			path == "/admin/plugin/rolling-memory/background/select" {
			c.Next()
			return
		}

		home, err := os.UserHomeDir()
		if err == nil {
			if !isLocalModeForHome(home) {
				err = fmt.Errorf("console direct vault access requires local_plain")
			} else {
				err = checkConsoleVaultScope(home)
				if err == nil && c.Request.Method != http.MethodGet {
					err = checkConsoleRequestScope(c)
				}
			}
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    http.StatusForbidden,
				"message": "local_plain vault scope verification failed",
				"error":   "local_plain vault scope verification failed",
			})
			return
		}
		c.Next()
	}
}

const maxConsoleScopeCheckBodyBytes = 4 << 20

func checkConsoleRequestScope(c *gin.Context) error {
	if c.Request.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxConsoleScopeCheckBodyBytes+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > maxConsoleScopeCheckBodyBytes {
		return fmt.Errorf("request body exceeds limit")
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var payload struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if scope := strings.TrimSpace(payload.Scope); scope != "" && scope != UserScope && scope != LegacyUserScope {
		return fmt.Errorf("requested scope is not supported by the console")
	}
	return nil
}

// resolveBoundVaultScope reads the local_plain_scope binding from thin_vault_meta so CLI
// subprocesses (--scope) receive the exact personal scope identifier ("default" or "mem:self")
// bound in the SQLite vault.
func resolveBoundVaultScope(dbPath, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		fallback = UserScope
	}
	if fallback != UserScope && fallback != LegacyUserScope {
		return fallback
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fallback
	}
	db := openVaultReadOnly(dbPath)
	if db == nil {
		return fallback
	}
	defer db.Close()
	var bound, storageType string
	if err := db.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType); err == nil {
		bound = strings.TrimSpace(bound)
		if storageType == "text" && (bound == UserScope || bound == LegacyUserScope) {
			return bound
		}
	}
	return fallback
}

func checkConsoleVaultScope(home string) error {
	var cfg struct {
		Core struct {
			Scope string `toml:"scope"`
		} `toml:"core"`
		Cloud struct {
			Sync struct {
				ScopeFromWorkspace bool `toml:"scope_from_workspace"`
			} `toml:"sync"`
		} `toml:"cloud"`
	}
	path := config.RollingConfigPath()
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := toml.Unmarshal(raw, &cfg); err != nil {
			return err
		}
	}
	scope := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_SCOPE"))
	if scope == "" {
		scope = strings.TrimSpace(cfg.Core.Scope)
	}
	if scope == "" {
		scope = UserScope
	}
	if cfg.Cloud.Sync.ScopeFromWorkspace && (scope == UserScope || scope == LegacyUserScope) {
		return fmt.Errorf("workspace-derived scope is not supported by the console")
	}
	// The console serves the user's own vault in either supported binding
	// (default, or the legacy mem:self that existing installs carry); the
	// anti-misattribution core is the binding match below, not the literal value.
	if scope != UserScope && scope != LegacyUserScope {
		return fmt.Errorf("configured scope is not supported by the console")
	}

	dbPath := resolveMemoryDBPath(home)
	if _, err := os.Stat(dbPath); err != nil {
		return err
	}
	db := openVaultReadOnly(dbPath)
	if db == nil {
		return fmt.Errorf("cannot open local_plain vault")
	}
	defer db.Close()
	var bound, storageType string
	if err := db.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("local_plain vault has no scope binding")
		}
		return err
	}
	if storageType != "text" || bound != scope {
		return fmt.Errorf("local_plain vault scope does not match its binding")
	}
	return nil
}
