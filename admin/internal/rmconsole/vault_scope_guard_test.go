package rollingmemory

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestConsoleVaultScopeGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		binding    any
		configured string
		wantStatus int
	}{
		{name: "matching scope", binding: "default", wantStatus: http.StatusOK},
		{name: "different binding", binding: "private", wantStatus: http.StatusForbidden},
		{name: "missing binding", wantStatus: http.StatusForbidden},
		{name: "invalid binding type", binding: []byte("default"), wantStatus: http.StatusForbidden},
		{name: "different configured scope", binding: "default", configured: "private", wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ROLLING_MEMORY_CONFIG", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("ROLLING_MEMORY_MODE", "local")
			t.Setenv("ROLLING_MEMORY_SCOPE", tc.configured)
			dbPath := filepath.Join(home, "vault.sqlite")
			t.Setenv("ROLLING_MEMORY_DB", dbPath)
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL); CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY, raw_text TEXT); INSERT INTO thin_vault_docs VALUES ('secret', 'private content')`); err != nil {
				t.Fatal(err)
			}
			if tc.binding != nil {
				if _, err := db.Exec(`INSERT INTO thin_vault_meta VALUES ('local_plain_scope', ?)`, tc.binding); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			router := gin.New()
			group := router.Group("/admin/plugin/rolling-memory")
			group.Use(consoleVaultScopeGuard())
			group.POST("/memories/detail", func(c *gin.Context) { c.String(http.StatusOK, "private content") })
			group.POST("/layers/delete", func(c *gin.Context) {
				wdb, err := sql.Open("sqlite", dbPath)
				if err != nil {
					c.Status(http.StatusInternalServerError)
					return
				}
				defer wdb.Close()
				if _, err := wdb.Exec(`DELETE FROM thin_vault_docs`); err != nil {
					c.Status(http.StatusInternalServerError)
					return
				}
				c.String(http.StatusOK, "deleted")
			})
			group.POST("/graph", func(c *gin.Context) { c.String(http.StatusOK, "private content") })
			paths := []string{"/memories/detail"}
			if tc.wantStatus != http.StatusOK {
				paths = append(paths, "/layers/delete")
			}
			for _, path := range paths {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory"+path, strings.NewReader(`{}`)))
				if w.Code != tc.wantStatus {
					t.Fatalf("%s status = %d, want %d: %s", path, w.Code, tc.wantStatus, w.Body.String())
				}
				if tc.wantStatus != http.StatusOK && strings.Contains(w.Body.String(), "private content") {
					t.Fatal("scope guard exposed vault data")
				}
			}
			if tc.wantStatus == http.StatusOK {
				for _, body := range []string{`{"scope":"private"}`, `{"Scope":"private"}`, `{"scope":"default","Scope":"private"}`, `{"Scope":"default","scope":"private"}`, `{"scope":"default","scope":"private"}`} {
					w := httptest.NewRecorder()
					router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/graph", strings.NewReader(body)))
					if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "private content") {
						t.Fatalf("cross-scope request %s: status=%d body=%s", body, w.Code, w.Body.String())
					}
				}
			}
			db, err = sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_docs`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("vault changed: count=%d err=%v", count, err)
			}
		})
	}
}

func TestConsoleVaultScopeGuardRejectsConfiguredWorkspaceScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_SCOPE", "")
	path := filepath.Join(home, ".config", "rolling-memory", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[cloud.sync]\nscope_from_workspace = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkConsoleVaultScope(home); err == nil {
		t.Fatal("workspace-derived scope must fail closed")
	}
}
