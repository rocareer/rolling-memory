// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// zz_atm_digest_probe_test.go —— ATM 语料摘要只读探针（与冻结 manifest 对拍）。
//
// 用途：以现役代码路径复算 ATM 语料摘要（LoadATMSGMItems → atmCloudDocuments → CloudCorpusDigest），
// 与历史 manifest 的 corpus_sha256 对拍，判定 digest 输入面是否发生过静默变更。
//
// 用法：
//
//	ATM_DIGEST_DATA=./evaldata/atmbench \
//	  ATM_DIGEST_SCOPE=u10-mem-eval-atmbench-20261107t134838z-b62f \
//	  go test ./internal/eval -run TestZZATMDigestProbe -count=1 -v
package eval

import (
	"os"
	"testing"
)

func TestZZATMDigestProbe(t *testing.T) {
	dataDir := os.Getenv("ATM_DIGEST_DATA")
	if dataDir == "" {
		t.Skip("set ATM_DIGEST_DATA=<atmbench dir>")
	}
	scope := os.Getenv("ATM_DIGEST_SCOPE")
	if scope == "" {
		scope = "u10-mem-eval-atmbench-digest-probe"
	}
	items, _, _, err := LoadATMSGMItems(dataDir, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	docs := atmCloudDocuments(items)
	digest, err := CloudCorpusDigest(docs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scope=%s items=%d documents=%d corpus_sha256=%s", scope, len(items), len(docs), digest)
}
