// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package updatecheck resolves the newest published version of Rolling Memory
// and keeps a local, throttled update-state cache shared by the CLI, the MCP
// server and the web console.
//
// The check separates two questions that used to be conflated: whether a newer
// version exists (a tag / semantic version) and whether the channel actually
// carries installable artifacts (SHA256SUMS plus the platform asset). A tag
// without release assets is a valid "new version" answer with
// installable=false, not a transport error.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/updatechannel"
	"github.com/rocareer/rolling-memory/internal/updatesource"
	"github.com/rocareer/rolling-memory/internal/version"
)

const (
	// DefaultRepo is the public repository whose tags and releases define the
	// version channel.
	DefaultRepo = updatesource.GitHubRepo
	// DefaultAPIBase is the GitHub REST API root used for tag discovery.
	DefaultAPIBase = "https://api.github.com"
	// DefaultReleaseBase is the official flat release channel.
	DefaultReleaseBase = updatesource.GitHubReleaseBase
	// CheckInterval is how long a successful state remains fresh.
	CheckInterval = 6 * time.Hour
	// RetryInterval is the shorter backoff after a failed check.
	RetryInterval = 30 * time.Minute

	stateFileName = "update-state.json"
	lockFileName  = "update-state.lock"
)

// State is the persisted update snapshot. It is deliberately small: every
// surface (CLI hint, MCP, console WebSocket) reads the same file instead of
// performing its own network check.
type State struct {
	OK                  bool   `json:"ok"`
	Channel             string `json:"channel"`
	CurrentVersion      string `json:"current_version"`
	LatestTag           string `json:"latest_tag"`
	LatestRelease       string `json:"latest_release"`
	LatestCommit        string `json:"latest_commit,omitempty"`
	LatestCommitShort   string `json:"latest_commit_short,omitempty"`
	InstalledCommit     string `json:"installed_commit,omitempty"`
	UpdateAvailable     bool   `json:"update_available"`
	DowngradeAvailable  bool   `json:"downgrade_available"`
	AssetsAvailable     bool   `json:"assets_available"`
	AssetsChecked       bool   `json:"assets_checked"`
	Installable         bool   `json:"installable"`
	NeedsSourceBaseline bool   `json:"needs_source_baseline,omitempty"`
	InstallMethod       string `json:"install_method"`
	ReleaseBase         string `json:"release_base"`
	Source              string `json:"source"`
	CheckedAt           string `json:"checked_at"`
	CheckedAtUnix       int64  `json:"checked_at_unix"`
	Error               string `json:"error,omitempty"`
}

// Result carries the state plus the release manifest needed to install.
type Result struct {
	State
	Sums            map[string]string `json:"-"`
	ManifestVersion string            `json:"-"`
	AssetBase       string            `json:"-"`
	CLIAsset        string            `json:"-"`
	AdminAsset      string            `json:"-"`
}

// Options configures one check.
type Options struct {
	ReleaseBase      string
	APIBase          string
	GiteeAPIBase     string
	GiteeReleaseBase string
	Repo             string
	Channel          string
	CurrentVersion   string
	TagsOnly         bool
	NoCache          bool
	Timeout          time.Duration
}

type versionInfo struct {
	Tag        string
	ReleaseTag string
	Assets     bool
	Source     string
}

// Check resolves the latest version, optionally verifies the release manifest
// and persists the result. A tag-only check never touches the artifact
// channel; a full check records whether the newest tag is installable.
func Check(ctx context.Context, opts Options) (Result, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		repo = DefaultRepo
	}
	channel, channelErr := updatechannel.Normalize(opts.Channel)
	if channelErr != nil {
		return Result{}, channelErr
	}
	current := strings.TrimSpace(opts.CurrentVersion)
	if current == "" {
		current = version.Version
	}
	metadata := ReadInstallMetadata()
	base := strings.TrimSpace(opts.ReleaseBase)
	gitee := updatesource.IsGiteeBase(base)
	result := Result{
		State: State{
			CurrentVersion:  current,
			Channel:         channel,
			InstallMethod:   metadata.Method,
			InstalledCommit: metadata.Commit,
			ReleaseBase:     base,
			AssetsChecked:   !opts.TagsOnly,
		},
		CLIAsset:   PlatformCLIAssetName(),
		AdminAsset: PlatformAdminAssetName(),
	}
	if base == "" && !opts.TagsOnly && channel != updatechannel.Dev {
		return failState(result, errors.New("release channel is disabled (ROLLING_MEMORY_RELEASE_BASE is off)")), errors.New("release channel is disabled (ROLLING_MEMORY_RELEASE_BASE is off)")
	}

	if channel == updatechannel.Dev {
		installedCommit := strings.TrimSpace(metadata.Commit)
		if installedCommit == "" {
			// A release install has no source commit. Do not hit the network just
			// to repeat that fact: report the baseline requirement once and let the
			// user choose stable/beta or perform one source install.
			result.LatestTag = "main"
			result.AssetsChecked = false
			result.AssetsAvailable = false
			result.Installable = false
			result.NeedsSourceBaseline = true
			result.UpdateAvailable = false
			result.DowngradeAvailable = false
			result.Source = "baseline-required"
			result.OK = true
			markChecked(&result.State)
			if !opts.NoCache {
				_ = WriteState(result.State)
			}
			return result, nil
		}
		var head MainHead
		var err error
		if gitee {
			head, err = GiteeMainHead(ctx, opts.GiteeAPIBase, updatesource.GiteeRepoFromBase(base), opts.Timeout)
		} else {
			head, err = GitHubMainHead(ctx, opts.APIBase, repo, opts.Timeout)
		}
		if err != nil {
			message := fmt.Sprintf("no develop branch head found: %v", err)
			failed := failState(result, errors.New(message))
			return failed, errors.New(message)
		}
		result.LatestTag = "main"
		result.LatestCommit = head.SHA
		result.LatestCommitShort = head.ShortSHA
		result.AssetsChecked = true
		result.AssetsAvailable = false
		result.Installable = false
		if gitee {
			result.Source = "gitee-main"
		} else {
			result.Source = "github-main"
		}
		result.UpdateAvailable = !strings.EqualFold(installedCommit, head.SHA)
		// 本机 commit 与 main 不同时，再用提交时间区分“落后”与“本地领先”。
		// 仅当两端都有可解析时间时启用；install.json 没有 commit_date 时保持
		// 原有的 SHA 比较口径。
		if result.UpdateAvailable && strings.TrimSpace(metadata.CommitDate) != "" && strings.TrimSpace(head.Date) != "" {
			if installedAt, parseErr := time.Parse(time.RFC3339, metadata.CommitDate); parseErr == nil {
				if headAt, parseErr := time.Parse(time.RFC3339, head.Date); parseErr == nil {
					result.UpdateAvailable = headAt.After(installedAt)
				}
			}
		}
		result.DowngradeAvailable = false
		result.OK = true
		markChecked(&result.State)
		if !opts.NoCache {
			_ = WriteState(result.State)
		}
		return result, nil
	}

	assetBase := base
	var discoveryErr, manifestErr error
	if gitee {
		info, err := GiteeLatest(ctx, opts.GiteeAPIBase, updatesource.GiteeRepoFromBase(base), current, channel, opts.Timeout)
		if err != nil {
			discoveryErr = err
		} else {
			result.LatestTag = info.Tag
			result.LatestRelease = info.ReleaseTag
			result.AssetsAvailable = info.Assets
			result.Source = info.Source
			if info.Tag != "" {
				if strings.TrimSpace(opts.GiteeReleaseBase) != "" {
					assetBase = strings.TrimSuffix(strings.TrimSpace(opts.GiteeReleaseBase), "/") + "/" + url.PathEscape(info.Tag)
				} else {
					assetBase = updatesource.GiteeReleaseDownloadBase(updatesource.GiteeRepoFromBase(base), info.Tag)
				}
			}
		}
	} else if useGitHub(opts, base, repo) {
		info, err := GitHubLatest(ctx, opts.APIBase, repo, current, channel, opts.Timeout)
		if err != nil {
			discoveryErr = err
		} else {
			result.LatestTag = info.Tag
			result.LatestRelease = info.ReleaseTag
			result.AssetsAvailable = info.Assets
			result.Source = info.Source
			if channel == updatechannel.Beta && info.Tag != "" && officialReleaseBase(base, repo) {
				assetBase = githubReleaseDownloadBase(repo, info.Tag)
			}
		}
	}

	if !opts.TagsOnly || result.LatestTag == "" {
		sums, manifestVersion, err := FetchManifest(ctx, assetBase, opts.Timeout)
		if err != nil {
			manifestErr = err
		} else {
			result.Sums = sums
			result.ManifestVersion = manifestVersion
			result.AssetsChecked = true
			if result.LatestTag == "" || CompareSemver(manifestVersion, result.LatestTag) > 0 {
				result.LatestTag = manifestVersion
			}
			if sameVersion(manifestVersion, result.LatestTag) && sums[result.CLIAsset] != "" {
				result.AssetsAvailable = true
			}
			if result.LatestRelease == "" {
				result.LatestRelease = manifestVersion
			}
		}
	}

	if opts.TagsOnly && result.LatestTag == "" {
		if detected, err := fetchVersion(ctx, base, opts.Timeout); err == nil {
			result.LatestTag = detected
			result.ManifestVersion = detected
			result.Source = "release-version"
		}
	}
	if result.LatestTag == "" && gitee && strings.TrimSpace(opts.GiteeAPIBase) == "" {
		// Anonymous Gitee API calls can be rate-limited. A failed mirror
		// discovery must not disable updates when the official GitHub channel
		// is reachable, so retry the same request there once.
		retryOpts := opts
		retryOpts.ReleaseBase = updatesource.ReleaseBase(updatesource.GitHub)
		retryOpts.APIBase = ""
		retryOpts.GiteeAPIBase = ""
		retryOpts.GiteeReleaseBase = ""
		if retry, retryErr := Check(ctx, retryOpts); retryErr == nil || retry.LatestTag != "" {
			retry.Source = "github-fallback"
			return retry, retryErr
		}
	}
	if result.LatestTag == "" {
		message := joinErrors("no published version found", discoveryErr, manifestErr)
		failed := failState(result, errors.New(message))
		return failed, errors.New(message)
	}
	result.LatestTag = NormalizeVersion(result.LatestTag)
	result.LatestRelease = NormalizeVersion(result.LatestRelease)
	result.ManifestVersion = NormalizeVersion(result.ManifestVersion)
	result.UpdateAvailable = CompareSemver(current, result.LatestTag) < 0
	result.DowngradeAvailable = CompareSemver(current, result.LatestTag) > 0
	result.Installable = result.AssetsAvailable
	if opts.TagsOnly {
		if previous, ok := ReadState(); ok && sameVersion(previous.LatestTag, result.LatestTag) {
			result.AssetsAvailable = previous.AssetsAvailable
			result.AssetsChecked = previous.AssetsChecked
			result.Installable = previous.Installable
		}
	}
	result.AssetBase = assetBase
	result.OK = true
	result.Error = ""
	markChecked(&result.State)
	if !opts.NoCache {
		_ = WriteState(result.State)
	}
	return result, nil
}

func failState(result Result, err error) Result {
	result.OK = false
	result.Error = err.Error()
	markChecked(&result.State)
	if !result.State.ReleaseBaseEmpty() || result.State.Channel == updatechannel.Dev {
		_ = WriteState(result.State)
	}
	return result
}

// ReleaseBaseEmpty reports whether the state was produced without a channel.
func (s State) ReleaseBaseEmpty() bool { return strings.TrimSpace(s.ReleaseBase) == "" }

func markChecked(state *State) {
	now := time.Now().UTC()
	state.CheckedAt = now.Format(time.RFC3339)
	state.CheckedAtUnix = now.Unix()
}

func joinErrors(prefix string, errs ...error) string {
	parts := []string{prefix}
	for _, err := range errs {
		if err == nil || strings.TrimSpace(err.Error()) == "" {
			continue
		}
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, ": ")
}

func useGitHub(opts Options, base, repo string) bool {
	if env := strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_UPDATE_API_BASE"))); env != "" {
		switch env {
		case "off", "none", "0", "false", "disabled":
			return false
		default:
			return true
		}
	}
	if strings.TrimSpace(opts.APIBase) != "" {
		return true
	}
	return strings.Contains(strings.ToLower(base), strings.ToLower("github.com/"+repo))
}

func officialReleaseBase(base, repo string) bool {
	needle := "github.com/" + strings.ToLower(repo) + "/releases/latest/download"
	return strings.Contains(strings.ToLower(base), needle)
}

func githubReleaseDownloadBase(repo, tag string) string {
	return "https://github.com/" + repo + "/releases/download/" + url.PathEscape(strings.TrimSpace(tag))
}

// GitHubLatest returns the newest semantic version tag (including tags that do
// not have a Release object) and whether that tag has a verifiable manifest.
func GitHubLatest(ctx context.Context, apiBase, repo, current, channel string, timeout time.Duration) (versionInfo, error) {
	if err := validateRepo(repo); err != nil {
		return versionInfo{}, err
	}
	base := strings.TrimSuffix(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	if err := validateAPIBase(base); err != nil {
		return versionInfo{}, err
	}
	allowPrerelease := channel == updatechannel.Beta

	var releases []githubRelease
	if err := getJSON(ctx, base+"/repos/"+repo+"/releases?per_page=30", timeout, &releases); err == nil {
		best := githubRelease{}
		found := false
		for _, release := range releases {
			if release.Draft {
				continue
			}
			if release.Prerelease && !allowPrerelease {
				continue
			}
			if !found || CompareSemver(release.TagName, best.TagName) > 0 {
				best = release
				found = true
			}
		}
		if found {
			return versionInfo{
				Tag:        best.TagName,
				ReleaseTag: best.TagName,
				Assets:     releaseHasManifest(best),
				Source:     "github-api",
			}, nil
		}
	}

	var tags []githubTag
	if err := getJSON(ctx, base+"/repos/"+repo+"/tags?per_page=30", timeout, &tags); err == nil {
		best := ""
		for _, tag := range tags {
			if strings.TrimSpace(tag.Name) == "" {
				continue
			}
			if strings.Contains(tag.Name, "-") && !allowPrerelease {
				continue
			}
			if best == "" || CompareSemver(tag.Name, best) > 0 {
				best = tag.Name
			}
		}
		if best != "" {
			return versionInfo{Tag: best, ReleaseTag: "", Assets: false, Source: "github-tags"}, nil
		}
	}

	tag, err := latestReleaseRedirect(ctx, repo, timeout)
	if err != nil {
		return versionInfo{}, err
	}
	return versionInfo{Tag: tag, ReleaseTag: tag, Assets: false, Source: "github-redirect"}, nil
}

// GiteeLatest returns the newest semantic version tag published on the Gitee
// mirror. Gitee releases are optional: a tag without attached artifacts still
// answers the version question and leaves Installable false.
func GiteeLatest(ctx context.Context, apiBase, repo, current, channel string, timeout time.Duration) (versionInfo, error) {
	if err := validateRepo(repo); err != nil {
		return versionInfo{}, err
	}
	base := strings.TrimSuffix(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = updatesource.GiteeAPIBase
	}
	if err := validateAPIBase(base); err != nil {
		return versionInfo{}, err
	}
	allowPrerelease := channel == updatechannel.Beta
	// The dedicated latest-release endpoint is one request and is less likely
	// to hit the anonymous tag-list rate limit. It answers the stable channel
	// directly; beta still needs the tag list to see prereleases.
	if !allowPrerelease {
		var release struct {
			TagName    string `json:"tag_name"`
			Prerelease bool   `json:"prerelease"`
			Draft      bool   `json:"draft"`
		}
		if err := getJSON(ctx, base+"/repos/"+repo+"/releases/latest", timeout, &release); err == nil {
			name := strings.TrimSpace(release.TagName)
			if name != "" && !release.Draft && !release.Prerelease && isSemverTag(name) {
				return versionInfo{Tag: name, ReleaseTag: name, Assets: false, Source: "gitee-release-latest"}, nil
			}
		}
	}
	var tags []githubTag
	if err := getJSON(ctx, base+"/repos/"+repo+"/tags?per_page=100&sort=name&direction=desc", timeout, &tags); err != nil {
		return versionInfo{}, err
	}
	best := ""
	for _, tag := range tags {
		name := strings.TrimSpace(tag.Name)
		if name == "" || !isSemverTag(name) {
			continue
		}
		if semverPrerelease(name) && !allowPrerelease {
			continue
		}
		if best == "" || CompareSemver(name, best) > 0 {
			best = name
		}
	}
	if best == "" {
		return versionInfo{}, errors.New("gitee returned no semantic version tag")
	}
	return versionInfo{Tag: best, ReleaseTag: best, Assets: false, Source: "gitee-tags"}, nil
}

func isSemverTag(raw string) bool {
	_, ok := parseSemver(raw)
	return ok
}

func semverPrerelease(raw string) bool {
	parsed, ok := parseSemver(raw)
	return ok && len(parsed.pre) > 0
}

// GiteeMainHead resolves the latest commit on the Gitee mirror's main branch.
func GiteeMainHead(ctx context.Context, apiBase, repo string, timeout time.Duration) (MainHead, error) {
	if err := validateRepo(repo); err != nil {
		return MainHead{}, err
	}
	base := strings.TrimSuffix(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = updatesource.GiteeAPIBase
	}
	if err := validateAPIBase(base); err != nil {
		return MainHead{}, err
	}
	var payload struct {
		SHA    string `json:"sha"`
		Commit struct {
			SHA     string `json:"sha"`
			Message string `json:"message"`
			Author  struct {
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := getJSON(ctx, base+"/repos/"+repo+"/branches/main", timeout, &payload); err != nil {
		if head, gitErr := gitRemoteMainHead(ctx, "https://gitee.com/"+repo+".git", timeout); gitErr == nil {
			return head, nil
		}
		return MainHead{}, err
	}
	sha := strings.TrimSpace(payload.Commit.SHA)
	if sha == "" {
		sha = strings.TrimSpace(payload.SHA)
	}
	if len(sha) < 7 {
		return MainHead{}, errors.New("gitee branch main returned no commit")
	}
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return MainHead{SHA: sha, ShortSHA: short, Date: payload.Commit.Author.Date, Message: payload.Commit.Message}, nil
}

// MainHead is the current head of the development branch.
type MainHead struct {
	SHA      string
	ShortSHA string
	Date     string
	Message  string
}

// GitHubMainHead resolves the latest commit on the repository's main branch.
func GitHubMainHead(ctx context.Context, apiBase, repo string, timeout time.Duration) (MainHead, error) {
	if err := validateRepo(repo); err != nil {
		return MainHead{}, err
	}
	base := strings.TrimSuffix(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	if err := validateAPIBase(base); err != nil {
		return MainHead{}, err
	}
	var payload struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := getJSON(ctx, base+"/repos/"+repo+"/commits/main", timeout, &payload); err != nil {
		if head, gitErr := gitMainHead(ctx, repo, timeout); gitErr == nil {
			return head, nil
		}
		return MainHead{}, err
	}
	sha := strings.TrimSpace(payload.SHA)
	if sha == "" {
		return MainHead{}, errors.New("main branch returned no commit SHA")
	}
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return MainHead{
		SHA:      sha,
		ShortSHA: short,
		Date:     strings.TrimSpace(payload.Commit.Author.Date),
		Message:  strings.TrimSpace(payload.Commit.Message),
	}, nil
}

func gitMainHead(ctx context.Context, repo string, timeout time.Duration) (MainHead, error) {
	return gitRemoteMainHead(ctx, "https://github.com/"+repo+".git", timeout)
}

func gitRemoteMainHead(ctx context.Context, remoteURL string, timeout time.Duration) (MainHead, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return MainHead{}, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(commandCtx, bin, "ls-remote", remoteURL, "refs/heads/main").Output()
	if err != nil {
		return MainHead{}, err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || len(fields[0]) < 7 {
		return MainHead{}, errors.New("git ls-remote returned no main commit")
	}
	sha := fields[0]
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return MainHead{SHA: sha, ShortSHA: short}, nil
}

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
}

type githubTag struct {
	Name string `json:"name"`
}

func releaseHasManifest(release githubRelease) bool {
	for _, asset := range release.Assets {
		if strings.EqualFold(strings.TrimSpace(asset.Name), "SHA256SUMS") {
			return true
		}
	}
	return false
}

func latestReleaseRedirect(ctx context.Context, repo string, timeout time.Duration) (string, error) {
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	location := response.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("release redirect returned HTTP %d without a location", response.StatusCode)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] != "tag" || parts[len(parts)-1] == "" {
		return "", fmt.Errorf("cannot parse release tag from %q", location)
	}
	return parts[len(parts)-1], nil
}

func getJSON(ctx context.Context, endpoint string, timeout time.Duration, target any) error {
	client := &http.Client{Timeout: timeout}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "rolling-memory-update-check")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("HTTP %d for %s", response.StatusCode, endpoint)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	return decoder.Decode(target)
}

func validateRepo(repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid repository %q", repo)
	}
	for _, part := range parts {
		for _, char := range part {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
				continue
			}
			return fmt.Errorf("invalid repository %q", repo)
		}
	}
	return nil
}

func validateAPIBase(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid update API base %q", raw)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("update API base must use https:// (or http:// on loopback)")
}

// FetchManifest downloads SHA256SUMS (and the optional VERSION file) from a
// flat release channel.
func FetchManifest(ctx context.Context, base string, timeout time.Duration) (map[string]string, string, error) {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, "", errors.New("release channel is disabled")
	}
	raw, err := httpGet(ctx, base+"/SHA256SUMS", timeout)
	if err != nil {
		return nil, "", err
	}
	sums, detected := ParseSHA256Sums(string(raw))
	if len(sums) == 0 {
		return nil, "", errors.New("empty or invalid SHA256SUMS")
	}
	if detected == "" {
		detected, _ = fetchVersion(ctx, base, 3*time.Second)
	}
	return sums, detected, nil
}

func fetchVersion(ctx context.Context, base string, timeout time.Duration) (string, error) {
	payload, err := httpGet(ctx, strings.TrimSuffix(base, "/")+"/VERSION", timeout)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(payload))
	if version == "" || strings.ContainsAny(version, " \t\r\n") {
		return "", errors.New("invalid VERSION file")
	}
	return version, nil
}

// ParseSHA256Sums parses a standard SHA256SUMS file into an asset map and
// extracts the version from a versioned archive name when present.
func ParseSHA256Sums(content string) (map[string]string, string) {
	sums := make(map[string]string)
	detected := ""
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sha := strings.ToLower(strings.TrimSpace(fields[0]))
		name := strings.TrimPrefix(strings.TrimSpace(fields[1]), "*")
		if len(sha) != 64 {
			continue
		}
		sums[name] = sha
		if strings.HasPrefix(name, "rolling-memory-") && strings.HasSuffix(name, suffix) {
			candidate := strings.TrimSuffix(strings.TrimPrefix(name, "rolling-memory-"), suffix)
			if candidate != "" && candidate != runtime.GOOS {
				detected = candidate
			}
		}
	}
	return sums, detected
}

// PlatformCLIAssetName returns the flat CLI asset name for this platform.
func PlatformCLIAssetName() string {
	name := "rolling-memory-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// PlatformAdminAssetName returns the flat console backend asset name.
func PlatformAdminAssetName() string {
	name := "rolling-admin-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func httpGet(ctx context.Context, endpoint string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", response.StatusCode, endpoint)
	}
	return io.ReadAll(io.LimitReader(response.Body, 256<<20))
}

// StatePath returns the shared cache path.
func StatePath() string {
	dir := strings.TrimSpace(config.DefaultStateDir())
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir = filepath.Join(home, ".local", "state", "rolling-memory")
		}
	}
	return filepath.Join(dir, stateFileName)
}

// ReadState loads the persisted snapshot.
func ReadState() (State, bool) {
	raw, err := os.ReadFile(StatePath())
	if err != nil {
		return State{}, false
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, false
	}
	if state.CheckedAtUnix == 0 && state.CheckedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, state.CheckedAt); err == nil {
			state.CheckedAtUnix = parsed.Unix()
		}
	}
	return state, true
}

// WriteState atomically persists the snapshot.
func WriteState(state State) error {
	path := StatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary := path + ".tmp." + fmt.Sprint(os.Getpid())
	if err := os.WriteFile(temporary, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// InstallMetadata is the provenance written by install.sh / install.ps1.
type InstallMetadata struct {
	Method     string `json:"method"`
	Repo       string `json:"repo"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
	CommitDate string `json:"commit_date,omitempty"`
	Asset      string `json:"asset"`
}

// ReadInstallMetadata reads the install provenance. When install.json is
// absent (manual builds, go install), the Go build's embedded VCS revision is
// used as a read-only baseline so the dev channel can still compare main.
func ReadInstallMetadata() InstallMetadata {
	path := filepath.Join(DataDir(), "install.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return installMetadataWithBuildInfo(InstallMetadata{Method: "unknown"})
	}
	var meta InstallMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return installMetadataWithBuildInfo(InstallMetadata{Method: "unknown"})
	}
	meta.Method = strings.ToLower(strings.TrimSpace(meta.Method))
	switch meta.Method {
	case "release", "source", "local", "go-install":
	default:
		meta.Method = "unknown"
	}
	return installMetadataWithBuildInfo(meta)
}

func installMetadataWithBuildInfo(meta InstallMetadata) InstallMetadata {
	if strings.TrimSpace(meta.Commit) != "" || IsTestBinary() {
		return meta
	}
	revision, commitTime := buildInfoVCS()
	if revision == "" {
		return meta
	}
	meta.Commit = revision
	if strings.TrimSpace(meta.CommitDate) == "" {
		meta.CommitDate = commitTime
	}
	return meta
}

func buildInfoVCS() (string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	return vcsFromBuildInfo(info)
}

func vcsFromBuildInfo(info *debug.BuildInfo) (string, string) {
	if info == nil {
		return "", ""
	}
	var revision, commitTime string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.time":
			commitTime = strings.TrimSpace(setting.Value)
		}
	}
	return revision, commitTime
}

// ResolveChannel resolves an explicit flag, environment, config, then the
// stable default.
func ResolveChannel(flag string) string {
	if value, err := updatechannel.Normalize(flag); err == nil && strings.TrimSpace(flag) != "" {
		return value
	}
	if env := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_UPDATE_CHANNEL")); env != "" {
		if value, err := updatechannel.Normalize(env); err == nil {
			return value
		}
	}
	if cfg, err := config.Load(config.LoadOptions{}); err == nil {
		if value, err := updatechannel.Normalize(cfg.Update.Channel); err == nil {
			return value
		}
	}
	return updatechannel.Default
}

// DataDir returns the data directory shared with the SQLite vault.
func DataDir() string {
	dir := filepath.Dir(config.DefaultDBPath())
	if strings.TrimSpace(dir) == "" || dir == "." {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir = filepath.Join(home, ".local", "share", "rolling-memory")
		}
	}
	return dir
}

// AutoCheckEnabled reports whether background checks are allowed. Explicit
// `update --check` commands ignore this switch.
func AutoCheckEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_UPDATE_CHECK"))) {
	case "off", "none", "0", "false", "disabled":
		return false
	default:
		return true
	}
}

// IsTestBinary keeps background checks out of `go test` runs.
func IsTestBinary() bool {
	name := strings.TrimSpace(os.Args[0])
	return strings.HasSuffix(name, ".test") || strings.Contains(name, ".test.")
}

// MaybeRefresh performs a throttled background check when the cached snapshot
// is stale. It is safe to call from short-lived MCP sessions: a lock file keeps
// concurrent sessions from stampeding the network, and a failed check backs off
// for RetryInterval.
func MaybeRefresh(ctx context.Context, opts Options) {
	if !AutoCheckEnabled() || IsTestBinary() {
		return
	}
	if strings.TrimSpace(opts.ReleaseBase) == "" {
		opts.ReleaseBase = ReleaseBaseFromEnv()
	}
	if strings.TrimSpace(opts.ReleaseBase) == "" {
		return
	}
	if strings.TrimSpace(opts.Channel) == "" {
		opts.Channel = ResolveChannel("")
	}
	if state, ok := ReadState(); ok {
		interval := CheckInterval
		if !state.OK {
			interval = RetryInterval
		}
		if time.Since(time.Unix(state.CheckedAtUnix, 0)) < interval {
			return
		}
	}
	unlock, ok := acquireLock()
	if !ok {
		return
	}
	defer unlock()
	if state, ok := ReadState(); ok {
		interval := CheckInterval
		if !state.OK {
			interval = RetryInterval
		}
		if time.Since(time.Unix(state.CheckedAtUnix, 0)) < interval {
			return
		}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, opts.Timeout+10*time.Second)
	defer cancel()
	_, _ = Check(checkCtx, opts)
}

// Hint returns the cached update summary for read-only surfaces (status,
// doctor) without performing any network request.
func Hint() map[string]any {
	state, ok := ReadState()
	if !ok || !state.OK || !state.UpdateAvailable {
		return nil
	}
	latest := "v" + strings.TrimPrefix(state.LatestTag, "v")
	if state.Channel == "dev" && strings.TrimSpace(state.LatestCommitShort) != "" {
		latest = "main@" + strings.TrimSpace(state.LatestCommitShort)
	}
	return map[string]any{
		"available":       true,
		"channel":         state.Channel,
		"current_version": state.CurrentVersion,
		"latest_tag":      state.LatestTag,
		"latest_display":  latest,
		"installable":     state.Installable,
		"checked_at":      state.CheckedAt,
	}
}

// ReleaseBaseFromEnv mirrors the CLI's release-channel resolution for
// background callers that do not parse flags.
func ReleaseBaseFromEnv() string {
	base, configured := os.LookupEnv("ROLLING_MEMORY_RELEASE_BASE")
	if configured {
		switch strings.ToLower(strings.TrimSpace(base)) {
		case "", "off", "none", "0":
			return ""
		}
		return strings.TrimSpace(base)
	}
	if cfg, err := config.Load(config.LoadOptions{}); err == nil {
		return updatesource.ReleaseBase(cfg.Update.Source)
	}
	return DefaultReleaseBase
}

func acquireLock() (func(), bool) {
	path := StatePath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false
	}
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if checkLockAbandoned(path) {
			_ = os.Remove(path)
			handle, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		}
		if err != nil {
			return nil, false
		}
	}
	_, _ = handle.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
	_ = handle.Close()
	return func() { _ = os.Remove(path) }, true
}

// checkLockAbandoned reports whether an existing state-cache lock no longer has
// a live owner and can be reclaimed. A lock older than ten minutes is always
// reclaimable; a younger lock is reclaimable only when it records a positive
// PID that no longer refers to a running process. An unreadable or empty lock
// is treated as live so a writer that has not yet recorded its PID is never
// raced.
func checkLockAbandoned(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	if time.Since(info.ModTime()) > 10*time.Minute {
		return true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	return !processAlive(pid)
}

// processAlive reports whether a same-host process is still running via a
// signal-0 probe. Only ESRCH / a reaped process count as dead; permission
// errors or platforms without signal 0 are treated as alive so a stale lock is
// never reclaimed from a live owner.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return false
		}
		return true
	}
	return true
}
