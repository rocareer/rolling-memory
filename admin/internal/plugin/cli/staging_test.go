package cli

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// TestStageZipProtectionszip 防护逐条：路径穿越 / 绝对路径 / 反斜杠 / 符号链接 /
// 判重（大小写折叠 + NFC）——一律拒装且不留解包临时目录。
func TestStageZipProtections(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
		want    string // 期望错误里出现的关键词
	}{
		{
			name:    "绝对路径",
			entries: []zipEntry{{name: "/etc/passwd", content: []byte("x")}},
			want:    "绝对路径",
		},
		{
			name:    "上级穿越",
			entries: []zipEntry{{name: "../evil.txt", content: []byte("x")}},
			want:    "规范化相对路径",
		},
		{
			name:    "深层穿越",
			entries: []zipEntry{{name: "a/../../evil.txt", content: []byte("x")}},
			want:    "规范化相对路径",
		},
		{
			name:    "反斜杠穿越",
			entries: []zipEntry{{name: `..\evil.txt`, content: []byte("x")}},
			want:    "反斜杠",
		},
		{
			name:    "符号链接",
			entries: []zipEntry{{name: "link", content: []byte("plugin.yaml"), mode: os.ModeSymlink | 0o777}},
			want:    "不是普通文件",
		},
		{
			name: "大小写折叠判重",
			entries: []zipEntry{
				{name: "Views/Index.vue", content: []byte("a")},
				{name: "views/index.vue", content: []byte("b")},
			},
			want: "判重冲突",
		},
		{
			name: "NFC 归一化判重",
			entries: []zipEntry{
				{name: "caf\u00e9.txt", content: []byte("a")},  // NFC：é 为预组合字符
				{name: "cafe\u0301.txt", content: []byte("b")}, // NFD：e + 组合重音符
			},
			want: "判重冲突",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := stageDirs(t)
			pkg := writeTestZip(t, tc.entries)
			staged, err := StageZip(pkg)
			if err == nil {
				staged.Cleanup()
				t.Fatalf("应拒装（%s），实际通过", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息应含 %q，实际 %q", tc.want, err.Error())
			}
			assertNoStageResidue(t, before)
		})
	}
}

// TestStageZipLimits 条目数与单文件字节上限（大小以**实际解出字节**计量，不看声明值）
func TestStageZipLimits(t *testing.T) {
	t.Run("条目数上限", func(t *testing.T) {
		entries := make([]zipEntry, 0, MaxEntryCount+1)
		for i := 0; i <= MaxEntryCount; i++ {
			entries = append(entries, zipEntry{name: fmt.Sprintf("dir%04d/f.txt", i), content: []byte("x")})
		}
		pkg := writeTestZip(t, entries)
		staged, err := StageZip(pkg)
		if err == nil {
			staged.Cleanup()
			t.Fatal("条目数超限应拒装")
		}
		if !strings.Contains(err.Error(), "条目数") {
			t.Errorf("错误信息应点名条目数上限，实际 %q", err.Error())
		}
	})

	t.Run("单文件上限", func(t *testing.T) {
		big := bytes.Repeat([]byte("A"), int(MaxEntryBytes)+1024)
		pkg := writeTestZip(t, []zipEntry{
			{name: "plugin.yaml", content: []byte("name: x\n")},
			{name: "big.bin", content: big},
		})
		staged, err := StageZip(pkg)
		if err == nil {
			staged.Cleanup()
			t.Fatal("单文件超限应拒装")
		}
		if !strings.Contains(err.Error(), "单文件上限") {
			t.Errorf("错误信息应点名单文件上限，实际 %q", err.Error())
		}
	})

	t.Run("整包上限", func(t *testing.T) {
		// 不可压缩内容（随机字节）撑过整包上限：stat 预检即拦，不必解包
		blob := make([]byte, int(MaxPackageBytes)+4096)
		if _, err := rand.Read(blob); err != nil {
			t.Fatal(err)
		}
		pkg := writeTestZip(t, []zipEntry{{name: "plugin.yaml", content: blob}})
		info, err := os.Stat(pkg)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() <= MaxPackageBytes {
			t.Fatalf("测试包未超过整包上限（%d 字节，需 > %d 字节）", info.Size(), MaxPackageBytes)
		}
		staged, err := StageZip(pkg)
		if err == nil {
			staged.Cleanup()
			t.Fatal("整包超限应拒装")
		}
		if !strings.Contains(err.Error(), "整包上限") {
			t.Errorf("错误信息应点名整包上限，实际 %q", err.Error())
		}
	})
}

// TestStageZipRejectsEncryptedEntry 安装链强制点之一：加密条目（通用标志位 0x1）一律拒。
// 必须落在 StageZip（而不是 catalog 层的包预检）：`aigo plugin install <zip>` 开发通道
// 只经 StageZip，而 Go 的 zip 读取层不做加密判定——不在这里拦，就要等 CRC 不符才晚失败。
func TestStageZipRejectsEncryptedEntry(t *testing.T) {
	pkg := writeTestZip(t, []zipEntry{
		{name: "plugin.yaml", content: []byte("name: x\n")},
		{name: "payload.bin", content: []byte("data")},
	})
	setZipEncryptedFlag(t, pkg)

	before := stageDirs(t)
	staged, err := StageZip(pkg)
	if err == nil {
		staged.Cleanup()
		t.Fatal("加密条目应拒装")
	}
	if !strings.Contains(err.Error(), "加密条目") {
		t.Errorf("错误信息应点名加密条目，实际 %q", err.Error())
	}
	assertNoStageResidue(t, before)
}

// TestStageZipRejectsCompressionBomb 安装链强制点之二：单条目解出 >1 MiB 且解出/压缩 >100 倍 ⇒ 拒。
// 三条边界一起钉住：超过阈值拒、阈值以下的同比值放行、不可压缩的大文件放行。
func TestStageZipRejectsCompressionBomb(t *testing.T) {
	t.Run("高压缩比且超过阈值", func(t *testing.T) {
		// 2 MiB 全零：deflate 后 ~2 KB ⇒ 比值远超 100，且解出量 > ratioCheckFloorBytes(1 MiB)
		bomb := make([]byte, 2<<20)
		pkg := writeTestZip(t, []zipEntry{
			{name: "plugin.yaml", content: []byte("name: x\n")},
			{name: "bomb.bin", content: bomb},
		})
		info, err := os.Stat(pkg)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > MaxPackageBytes {
			t.Fatalf("用量假设失效：测试包 %d 字节已超整包上限（本用例要打在压缩比上）", info.Size())
		}
		before := stageDirs(t)
		staged, err := StageZip(pkg)
		if err == nil {
			staged.Cleanup()
			t.Fatal("高压缩比条目应拒装")
		}
		if !strings.Contains(err.Error(), "解出/压缩比异常") {
			t.Errorf("错误信息应点名压缩比，实际 %q", err.Error())
		}
		assertNoStageResidue(t, before)
	})

	t.Run("同比值但未过阈值", func(t *testing.T) {
		// 512 KiB 全零：比值同样极高，但解出量 ≤ ratioCheckFloorBytes ⇒ 不拒（阈值语义：小文件比值噪声大）
		small := make([]byte, ratioCheckFloorBytes/2)
		pkg := writeTestZip(t, []zipEntry{
			{name: "plugin.yaml", content: []byte("name: x\n")},
			{name: "small.bin", content: small},
		})
		staged, err := StageZip(pkg)
		if err != nil {
			t.Fatalf("阈值以下的条目不应被压缩比检查拒: %v", err)
		}
		defer staged.Cleanup()
		if staged.Report.ExtractedBytes < ratioCheckFloorBytes/2 {
			t.Fatalf("解出读数不符: %+v", staged.Report)
		}
	})

	t.Run("大而不可压缩", func(t *testing.T) {
		blob := make([]byte, 2<<20)
		if _, err := rand.Read(blob); err != nil {
			t.Fatal(err)
		}
		pkg := writeTestZip(t, []zipEntry{
			{name: "plugin.yaml", content: []byte("name: x\n")},
			{name: "random.bin", content: blob},
		})
		staged, err := StageZip(pkg)
		if err != nil {
			t.Fatalf("不可压缩的大文件不应被压缩比检查拒: %v", err)
		}
		staged.Cleanup()
	})
}

// setZipEncryptedFlag 就地把 zip 里每条目的「通用目的标志」第 0 位置 1（加密标记）。
// 直接改字节而不靠 archive/zip 的写入侧：写入侧由库决定该位，改字节才与真实恶意包一致。
// 本地文件头 flags 在 +6，中央目录头 flags 在 +8（都会写，读取侧看中央目录）。
func setZipEncryptedFlag(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	patched := 0
	for i := 0; i+10 < len(data); i++ {
		if data[i] != 'P' || data[i+1] != 'K' {
			continue
		}
		switch {
		case data[i+2] == 0x03 && data[i+3] == 0x04: // 本地文件头
			data[i+6] |= 0x1
			patched++
		case data[i+2] == 0x01 && data[i+3] == 0x02: // 中央目录头
			data[i+8] |= 0x1
			patched++
		}
	}
	if patched == 0 {
		t.Fatal("未在 zip 里找到可打标记的头部")
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// 自证：改完必须真能读成加密条目（否则用例是空转）
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) == 0 || reader.File[0].Flags&zipFlagEncrypted == 0 {
		t.Fatal("改字节后读取侧仍未看到加密标记")
	}
}

// TestCheckEntryName 条目名的编码与形态防护（NUL / 非 UTF-8 / 盘符 / 非规范路径）
func TestCheckEntryName(t *testing.T) {
	bad := []string{
		"",
		"a\x00b.txt",
		"caf\xff.txt", // 非 UTF-8
		"C:/x.txt",
		"./x.txt",
		"a/./b.txt",
		"a//b.txt",
	}
	for _, name := range bad {
		if err := checkEntryName(name); err == nil {
			t.Errorf("条目名 %q 应被拒", name)
		}
	}
	good := []string{"plugin.yaml", "migrations/1_init.up.sql", "web/views/index.vue", "中文目录/文件.yaml"}
	for _, name := range good {
		if err := checkEntryName(name); err != nil {
			t.Errorf("条目名 %q 应通过: %v", name, err)
		}
	}
}

// TestStageZipRoundTrip 正常包：导出 → 解包 → 读数与内容逐项一致 → 清理干净
func TestStageZipRoundTrip(t *testing.T) {
	root := t.TempDir()
	for _, lang := range []string{"zh-cn", "en"} {
		if err := os.MkdirAll(filepath.Join(root, "web", "src", "lang", lang), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := &Env{Root: root, Out: &bytes.Buffer{}}
	if err := Scaffold(env, "stagetest", ScaffoldOptions{Kind: "code"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	pkg, err := Export(env, "stagetest", "")
	if err != nil {
		t.Fatalf("打包应成功: %v", err)
	}

	before := stageDirs(t)
	staged, err := StageZip(pkg)
	if err != nil {
		t.Fatalf("解包应成功: %v", err)
	}
	if staged.Report.Entries == 0 || staged.Report.ExtractedBytes == 0 {
		t.Errorf("解包读数不应为零: %+v", staged.Report)
	}
	if staged.Report.Checksum == "" {
		t.Error("解包读数应带整包 sha256")
	}
	content, err := os.ReadFile(filepath.Join(staged.Dir, manifest.FileName))
	if err != nil {
		t.Fatalf("解包目录内应有 %s: %v", manifest.FileName, err)
	}
	if !strings.Contains(string(content), "name: stagetest") {
		t.Errorf("%s 内容不符: %s", manifest.FileName, content)
	}

	dir := staged.Dir
	staged.Cleanup()
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Cleanup 后临时目录应消失: %v", err)
	}
	assertNoStageResidue(t, before)
}

// TestOwnershipMarker 归属标记：按扩展名选注释载体、幂等、无注释语法的格式不动
func TestOwnershipMarker(t *testing.T) {
	included := map[string]string{
		"a.go": "//", "a.ts": "//", "a.vue": "<!--", "a.md": "<!--",
		"a.yaml": "#", "a.yml": "#", "a.sql": "--", "a.css": "/*",
	}
	for rel, head := range included {
		marker := OwnershipMarker(rel, "demo", "1.0.0")
		if !strings.HasPrefix(marker, head) || !strings.Contains(marker, "plugin: demo v1.0.0") {
			t.Errorf("%s 的标记形态不符: %q", rel, marker)
		}
		first := ApplyOwnershipMarker(rel, []byte("body\n"), "demo", "1.0.0")
		if !strings.HasPrefix(string(first), marker) || !strings.HasSuffix(string(first), "body\n") {
			t.Errorf("%s 应在头部写入标记: %q", rel, first)
		}
		second := ApplyOwnershipMarker(rel, first, "demo", "1.0.0")
		if string(second) != string(first) {
			t.Errorf("%s 的标记写入应幂等（源↔副本 sha256 比对的前提）: %q", rel, second)
		}
	}
	for _, rel := range []string{"data.json", "icon.png", "noext"} {
		if marker := OwnershipMarker(rel, "demo", "1.0.0"); marker != "" {
			t.Errorf("%s 不在白名单内，不应写标记: %q", rel, marker)
		}
		content := []byte(`{"a":1}`)
		if got := ApplyOwnershipMarker(rel, content, "demo", "1.0.0"); string(got) != string(content) {
			t.Errorf("%s 不应被改写: %q", rel, got)
		}
	}
}

// TestDeployerPlaceAndRollback 落位与补偿回滚的文件面（无需库）：
// 新建 → 回滚删除；被覆盖 → 回滚还原（不是抹掉）；落位写归属标记；软链目标拒装。
func TestDeployerPlaceAndRollback(t *testing.T) {
	root := t.TempDir()
	env := &Env{Root: root, Out: &bytes.Buffer{}}

	mustWrite(t, filepath.Join(root, "src", "plugin.yaml"), "name: dploy\n")
	mustWrite(t, filepath.Join(root, "src", "new.go"), "package dploy\n")
	mustWrite(t, filepath.Join(root, "src", "old.go"), "package dploy\n")
	// 目标已存在且内容不同：补偿时必须还原（而不是删除）
	mustWrite(t, filepath.Join(root, "plugins", "dploy", "old.go"), "package old\n")

	plan := &InstallPlan{
		Name:     "dploy",
		Manifest: &manifest.Manifest{Name: "dploy", Version: "1.0.0"},
		Dir:      filepath.Join(root, "src"),
		Files: []PlanFile{
			{Rel: "plugin.yaml", Target: "plugins/dploy/plugin.yaml"},
			{Rel: "new.go", Target: "plugins/dploy/new.go"},
			{Rel: "old.go", Target: "plugins/dploy/old.go"},
		},
	}
	deployer, err := NewDeployer(env, plan)
	if err != nil {
		t.Fatalf("构造落位执行器失败: %v", err)
	}
	if err := deployer.Place(plan); err != nil {
		t.Fatalf("落位应成功: %v", err)
	}
	placed, err := os.ReadFile(filepath.Join(root, "plugins", "dploy", "new.go"))
	if err != nil {
		t.Fatalf("新文件应落位: %v", err)
	}
	if !strings.HasPrefix(string(placed), "// plugin: dploy v1.0.0\n") {
		t.Errorf("落位文件应带归属标记: %q", placed)
	}
	overwritten, err := os.ReadFile(filepath.Join(root, "plugins", "dploy", "old.go"))
	if err != nil {
		t.Fatalf("覆盖文件应落位: %v", err)
	}
	if !strings.Contains(string(overwritten), "plugin: dploy") {
		t.Errorf("覆盖文件应被新内容替换: %q", overwritten)
	}

	if err := deployer.Rollback(); err != nil {
		t.Fatalf("回滚应成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "dploy", "new.go")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("回滚应删除新建文件")
	}
	restored, err := os.ReadFile(filepath.Join(root, "plugins", "dploy", "old.go"))
	if err != nil {
		t.Fatalf("回滚应还原被覆盖文件: %v", err)
	}
	if string(restored) != "package old\n" {
		t.Errorf("回滚应还原覆盖前内容，实际 %q", restored)
	}
	deployer.Discard()

	// 软链目标：拒装（落位侧逐级拒软链）
	linkRoot := t.TempDir()
	linkEnv := &Env{Root: linkRoot, Out: &bytes.Buffer{}}
	mustWrite(t, filepath.Join(linkRoot, "src", "x.go"), "package x\n")
	if err := os.MkdirAll(filepath.Join(linkRoot, "plugins", "linkdemo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(linkRoot, "elsewhere"), filepath.Join(linkRoot, "plugins", "linkdemo", "x.go")); err != nil {
		t.Fatalf("建软链失败: %v", err)
	}
	linkPlan := &InstallPlan{
		Name:     "linkdemo",
		Manifest: &manifest.Manifest{Name: "linkdemo", Version: "1.0.0"},
		Dir:      filepath.Join(linkRoot, "src"),
		Files:    []PlanFile{{Rel: "x.go", Target: "plugins/linkdemo/x.go"}},
	}
	linkDeployer, err := NewDeployer(linkEnv, linkPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := linkDeployer.Place(linkPlan); err == nil {
		t.Fatal("落位目标为符号链接时应拒装")
	} else if !strings.Contains(err.Error(), "符号链接") {
		t.Errorf("错误信息应点名符号链接，实际 %q", err.Error())
	}
	linkDeployer.Discard()
}

// writeTestZip 写一个测试用 zip（mode 非零时按该 Mode 写条目头，用于造符号链接条目）
func writeTestZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "pkg.zip")
	handle, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(handle)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		item, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("写 zip 条目 %s 失败: %v", entry.name, err)
		}
		if _, err := item.Write(entry.content); err != nil {
			t.Fatalf("写 zip 条目 %s 失败: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}

// zipEntry 测试用 zip 条目
type zipEntry struct {
	name    string
	content []byte
	mode    fs.FileMode
}

// stageDirs 当前系统临时目录下的解包目录快照（用于「拒装不留残留」的确定性判定）
func stageDirs(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "aigo-plugin-stage-*"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(matches)
	return matches
}

// assertNoStageResidue 相对快照不应新增解包目录（拒装路径「中止并清理」的判定）
func assertNoStageResidue(t *testing.T, before []string) {
	t.Helper()
	for _, dir := range stageDirs(t) {
		if !slices.Contains(before, dir) {
			t.Errorf("拒装/清理后不应残留解包目录: %s", dir)
		}
	}
}
