// plugin: kb v1.0.0
package kb

import (
	"sync"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// ==================== ② 并发双授（aborted 事务回读回归） ====================

// TestAddGrantConcurrentDoubleGrantDB 同一 (library, subject, perm) 并发授权：唯一键冲突方必须在
// **事务外**回读先提交行（PostgreSQL 语句错误后事务已 aborted，同一 tx 内回读必失败）；四路应同 id、
// 只落 1 条 grant_add 审计。
func TestAddGrantConcurrentDoubleGrantDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	admin := Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}

	lib := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-race-"+f.suffix, 1)
	u := f.user("kb-acl-race-u-"+f.suffix, "enable", 0)
	in := GrantInput{SubjectKind: model.KBGrantSubjectUser, SubjectID: u.ID, Perm: model.KBGrantPermRead}

	const n = 4
	start := make(chan struct{})
	type result struct {
		id  uint
		err error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			g, err := f.svc.AddGrant(ctx, admin, lib.ID, in)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			results[i] = result{id: g.ID}
		}(i)
	}
	close(start)
	wg.Wait()

	var want uint
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("并发授权 #%d 失败（aborted 事务回读回归）: %v", i, r.err)
		}
		if want == 0 {
			want = r.id
		} else if r.id != want {
			t.Fatalf("并发双授应幂等回读同一行：%d vs %d", want, r.id)
		}
	}
	if want != 0 {
		f.grantIDs = append(f.grantIDs, want)
	}
	if n := f.auditCount(lib.ID, model.KBAuditActionGrantAdd, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("并发授权应恰留 1 条 grant_add 审计，得到 %d", n)
	}
}
