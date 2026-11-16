// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestLocalModeNeverContactsBrain(t *testing.T) {
	for _, credential := range []string{"explicit", "stored"} {
		t.Run(credential, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
			t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "")
			memory := newMemory(t)
			memory.Config.Cloud.Enabled = true
			memory.Config.Cloud.BaseURL = server.URL + "/open/memory/v1"
			if credential == "explicit" {
				t.Setenv("ROLLING_MEMORY_BRAIN_URL", memory.Config.Cloud.BaseURL)
				t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "test-key")
			} else {
				account := filepath.Join(home, ".config", "rolling-memory", "cloud-account.json")
				if err := os.MkdirAll(filepath.Dir(account), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(account, []byte(`{"base_url":"`+server.URL+`","api_key":"test-account"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if memory.IsThinBrainProxy() {
				t.Fatal("local mode selected remote proxy")
			}
			client, err := memory.ThinClient()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := client.Transport.(*thinclient.LocalVaultTransport); !ok {
				t.Fatalf("transport = %T", client.Transport)
			}
			if _, err := client.Remember(context.Background(), "local-sentinel", "local sentinel content", 1); err != nil {
				t.Fatal(err)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("local mode made %d remote requests", got)
			}
		})
	}
}

func TestThinClientBrainURLEnvPrecedence(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "http://127.0.0.1:18099/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "probe-key")
	memory := newMemory(t)
	memory.Config.Mode = "cloud"
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("ThinClient: %v", err)
	}
	transport, ok := client.Transport.(*thinclient.HTTPBrainTransport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if transport.BaseURL != "http://127.0.0.1:18099/open/memory/v1" {
		t.Fatalf("BaseURL = %q", transport.BaseURL)
	}
}
