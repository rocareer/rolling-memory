package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseGitLocation(t *testing.T) {
	ok := []struct {
		in      string
		url     string
		ref     string
		catPath string
	}{
		{"https://gitee.com/org/repo.git", "https://gitee.com/org/repo.git", "", DefaultGitCatalogPath},
		{"https://gitee.com/org/repo.git#main", "https://gitee.com/org/repo.git", "main", DefaultGitCatalogPath},
		{"https://gitee.com/org/repo.git!catalog/catalog.yaml", "https://gitee.com/org/repo.git", "", "catalog/catalog.yaml"},
		{"https://gitee.com/org/repo.git#v1.2.0!catalog/catalog.example.yaml", "https://gitee.com/org/repo.git", "v1.2.0", "catalog/catalog.example.yaml"},
		{"  https://example.com/a.git#release/1.0  ", "https://example.com/a.git", "release/1.0", DefaultGitCatalogPath},
	}
	for _, c := range ok {
		loc, err := ParseGitLocation(c.in)
		if err != nil {
			t.Fatalf("ParseGitLocation(%q) 失败: %v", c.in, err)
		}
		if loc.URL != c.url || loc.Ref != c.ref || loc.CatalogPath != c.catPath {
			t.Fatalf("ParseGitLocation(%q) = %+v，期望 url=%q ref=%q path=%q", c.in, loc, c.url, c.ref, c.catPath)
		}
	}

	bad := []string{
		"",
		"https://example.com/a.git#",
		"https://example.com/a.git!",
		"https://example.com/a.git!../escape.yaml",
		"https://example.com/a.git!/abs/catalog.yaml",
		"https://example.com/a.git#-bad-ref",
		"https://example.com/a.git#bad ref",
		"https://example.com/a.git#a..b",
		"#main",
	}
	for _, in := range bad {
		if _, err := ParseGitLocation(in); err == nil {
			t.Fatalf("ParseGitLocation(%q) 应当报错", in)
		}
	}
}

func TestValidateLocationGit(t *testing.T) {
	base := Source{Name: "mirror", Title: "镜像", Type: TypeGit, Enabled: 1}

	valid := base
	valid.Location = "https://gitee.com/org/repo.git#main!catalog/catalog.yaml"
	if err := valid.NormalizeAndValidate(); err != nil {
		t.Fatalf("合法 git 来源被拒: %v", err)
	}
	if valid.AllowAbsolutePaths != 0 {
		t.Fatalf("git 来源必须关闭 allow_absolute_paths，得到 %d", valid.AllowAbsolutePaths)
	}

	cases := []Source{
		{Name: "mirror", Title: "镜像", Type: TypeGit, Location: "", Enabled: 1},
		{Name: "mirror", Title: "镜像", Type: TypeGit, Location: "git@gitee.com:org/repo.git", Enabled: 1},
		{Name: "mirror", Title: "镜像", Type: TypeGit, Location: "https://user:pass@gitee.com/org/repo.git", Enabled: 1},
		{Name: "mirror", Title: "镜像", Type: TypeGit, Location: "http://gitee.com/org/repo.git", Enabled: 1},
		{Name: "mirror", Title: "镜像", Type: TypeGit, Location: "https://gitee.com/org/repo.git!../x.yaml", Enabled: 1},
	}
	for i, c := range cases {
		if err := c.NormalizeAndValidate(); err == nil {
			t.Fatalf("非法 git 来源 #%d 未被拒: %+v", i, c)
		}
	}

	// 明文 http：显式开关后形状放行（私网/取数防线仍在 FetchGitCatalog）
	insecure := base
	insecure.Location = "http://mirror.example.com/repo.git"
	insecure.AllowInsecureHTTP = 1
	if err := insecure.NormalizeAndValidate(); err != nil {
		t.Fatalf("开启 allow_insecure_http 的明文 git 来源仍被拒: %v", err)
	}
}

// TestFetchGitCatalogFromLocalRepo 用本地临时仓库验证「克隆 → 读仓内清单」链路（不起网络）。
func TestFetchGitCatalogFromLocalRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境没有 git，跳过")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", repo,
			"-c", "user.email=test@example.com", "-c", "user.name=test", "-c", "commit.gpgsign=false",
		}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	runGit("checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "catalog.yaml"), []byte(validCatalogYAML()), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "catalog.yaml")
	runGit("commit", "-q", "-m", "init")

	ctx := context.Background()
	doc, err := fetchGitCatalogInto(ctx, "unit-git", repo, "", "")
	if err != nil {
		t.Fatalf("fetchGitCatalogInto 失败: %v", err)
	}
	if doc == nil || doc.Catalog == nil || len(doc.Catalog.Plugins) != 1 {
		t.Fatalf("清单解析结果异常: %+v", doc)
	}
	if doc.SHA256 == "" || doc.BaseDir == "" {
		t.Fatalf("摘要/基准目录缺失: %+v", doc)
	}
	if doc.BaseDir != filepath.Dir(filepath.Join(gitCheckoutDir("unit-git"), "catalog.yaml")) {
		// BaseDir 走 EvalSymlinks（macOS 的 /var → /private/var），只断言末段目录名
		if filepath.Base(doc.BaseDir) != filepath.Base(gitCheckoutDir("unit-git")) && filepath.Base(doc.BaseDir) != "repo" {
			t.Fatalf("BaseDir 落点可疑: %s", doc.BaseDir)
		}
	}

	// 仓内没有清单 → ErrCatalogNotFound
	if _, err := fetchGitCatalogInto(ctx, "unit-git-missing", repo, "", "nope/catalog.yaml"); !errors.Is(err, ErrCatalogNotFound) {
		t.Fatalf("缺清单应返回 ErrCatalogNotFound，得到 %v", err)
	}
	// 越界路径在形状层被拒
	if _, err := fetchGitCatalogInto(ctx, "unit-git-escape", repo, "", "../catalog.yaml"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("越界路径应返回 ErrInvalidInput，得到 %v", err)
	}
}
