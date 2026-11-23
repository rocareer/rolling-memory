// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/updatechannel"
)

func TestGiteeLatestSelectsSemverByChannel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/Rolling-Memory/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"name":"v0.0.7-beta.1"},
			{"name":"v0.0.6"},
			{"name":"v0.0.5"},
			{"name":"nightly"}
		]`))
	}))
	defer server.Close()

	stable, err := GiteeLatest(context.Background(), server.URL, "acme/Rolling-Memory", "0.0.5", updatechannel.Stable, 2*time.Second)
	if err != nil {
		t.Fatalf("stable GiteeLatest: %v", err)
	}
	if stable.Tag != "v0.0.6" || stable.Source != "gitee-tags" {
		t.Fatalf("stable result = %#v", stable)
	}

	beta, err := GiteeLatest(context.Background(), server.URL, "acme/Rolling-Memory", "0.0.5", updatechannel.Beta, 2*time.Second)
	if err != nil {
		t.Fatalf("beta GiteeLatest: %v", err)
	}
	if beta.Tag != "v0.0.7-beta.1" {
		t.Fatalf("beta tag = %q, want v0.0.7-beta.1", beta.Tag)
	}
}

func TestCheckGiteeUsesTagSpecificManifest(t *testing.T) {
	manifest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  " + PlatformCLIAssetName() + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/Rolling-Memory/tags":
			_, _ = w.Write([]byte(`[{"name":"v0.0.7"},{"name":"v0.0.6"}]`))
		case "/download/v0.0.7/SHA256SUMS":
			_, _ = w.Write([]byte(manifest))
		case "/download/v0.0.7/VERSION":
			_, _ = w.Write([]byte("0.0.7\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	result, err := Check(context.Background(), Options{
		Channel:          updatechannel.Stable,
		ReleaseBase:      "https://gitee.com/acme/Rolling-Memory/releases/latest/download",
		GiteeAPIBase:     server.URL,
		GiteeReleaseBase: server.URL + "/download",
		CurrentVersion:   "0.0.6",
		Timeout:          2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.LatestTag != "0.0.7" || !result.UpdateAvailable {
		t.Fatalf("result = %#v", result.State)
	}
	if result.Source != "gitee-tags" || !result.AssetsAvailable || !result.Installable {
		t.Fatalf("assets = %#v", result.State)
	}
}

func TestGiteeMainHead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/Rolling-Memory/branches/main" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"name":"main","commit":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`))
	}))
	defer server.Close()

	head, err := GiteeMainHead(context.Background(), server.URL, "acme/Rolling-Memory", 2*time.Second)
	if err != nil {
		t.Fatalf("GiteeMainHead: %v", err)
	}
	if head.SHA != "0123456789abcdef0123456789abcdef01234567" || head.ShortSHA != "0123456789ab" {
		t.Fatalf("head = %#v", head)
	}
}

func TestGiteeLatestPrefersLatestRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/Rolling-Memory/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v0.0.14","prerelease":false,"draft":false}`))
		case "/repos/acme/Rolling-Memory/tags":
			_, _ = w.Write([]byte(`[{"name":"v0.0.15-beta.1"},{"name":"v0.0.13"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stable, err := GiteeLatest(context.Background(), server.URL, "acme/Rolling-Memory", "0.0.12", updatechannel.Stable, 2*time.Second)
	if err != nil {
		t.Fatalf("stable GiteeLatest: %v", err)
	}
	if stable.Tag != "v0.0.14" || stable.Source != "gitee-release-latest" {
		t.Fatalf("stable result = %#v", stable)
	}

	beta, err := GiteeLatest(context.Background(), server.URL, "acme/Rolling-Memory", "0.0.12", updatechannel.Beta, 2*time.Second)
	if err != nil {
		t.Fatalf("beta GiteeLatest: %v", err)
	}
	if beta.Tag != "v0.0.15-beta.1" {
		t.Fatalf("beta tag = %q, want v0.0.15-beta.1", beta.Tag)
	}
}
