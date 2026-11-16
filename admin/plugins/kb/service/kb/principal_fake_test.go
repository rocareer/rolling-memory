// plugin: kb v1.0.0
// 测试本地假体：user 轴主体面端口（Principal）的内存实现。
//
// 为什么需要它：本插件不假设宿主有用户体系（不建也不读 users/user_groups 类表），user 轴的用例
// 因此不能靠「建宿主用户行」来布景。假体承担两件事：
//  1. 让既有隔离语义用例（owner ∪ grant 可见性、团队授权、三种主体、撤权即时生效、软删库不可见、
//     团队巡检的组名→现行 id 回收检测）在**不依赖宿主表**的前提下继续覆盖；
//  2. 提供 `unavailable` 开关，用来断言「未接入用户体系」的部署里 user 轴 fail-closed、admin 轴不变。
package kb

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// fakePrincipalGrp 假体里的组（enabled 对应 user_groups.status=1）
type fakePrincipalGrp struct {
	ID      uint
	Name    string
	Enabled bool
}

// fakePrincipalUsr 假体里的用户
type fakePrincipalUsr struct {
	ID     uint
	Name   string
	Active bool
}

// fakePrincipal Principal 端口的内存实现（并发安全：用例里可能并发读视图）
type fakePrincipal struct {
	mu sync.Mutex

	nextUserID  uint
	nextGroupID uint

	users  map[uint]*fakePrincipalUsr
	groups map[uint]*fakePrincipalGrp

	// mainGroup 主分组（userID → groupID；0 = 无）
	mainGroup map[uint]uint
	// extraGroups 附加组（userID → groupID 集）
	extraGroups map[uint][]uint

	// unavailable 端口不可用开关（模拟「未接入用户体系」的部署）
	unavailable bool
}

// newFakePrincipal 新建假体（id 从 1 起，与宿主自增空间无关）
func newFakePrincipal() *fakePrincipal {
	return &fakePrincipal{
		users:       map[uint]*fakePrincipalUsr{},
		groups:      map[uint]*fakePrincipalGrp{},
		mainGroup:   map[uint]uint{},
		extraGroups: map[uint][]uint{},
	}
}

// setUnavailable 切换端口可用性（true = 每个方法都返回 fail-closed 错误）
func (f *fakePrincipal) setUnavailable(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable = v
}

// addGroup 建组（返回组视图；enabled 对应 status=1）
func (f *fakePrincipal) addGroup(name string, enabled bool) fakePrincipalGrp {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextGroupID++
	g := &fakePrincipalGrp{ID: f.nextGroupID, Name: name, Enabled: enabled}
	f.groups[g.ID] = g
	return *g
}

// addUser 建用户（mainGroupID=0 = 无主分组）
func (f *fakePrincipal) addUser(name string, active bool, mainGroupID uint) fakePrincipalUsr {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextUserID++
	u := &fakePrincipalUsr{ID: f.nextUserID, Name: name, Active: active}
	f.users[u.ID] = u
	if mainGroupID > 0 {
		f.mainGroup[u.ID] = mainGroupID
	}
	return *u
}

// addAccess 追加附加组关系
func (f *fakePrincipal) addAccess(userID, groupID uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extraGroups[userID] = append(f.extraGroups[userID], groupID)
}

// deleteGroup 硬删组（组不存在 ⇒ 所有引用它的成员关系随之失效）
func (f *fakePrincipal) deleteGroup(groupID uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.groups, groupID)
}

// setUserActive 切换用户启用态
func (f *fakePrincipal) setUserActive(userID uint, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[userID]; ok {
		u.Active = active
	}
}

// errFakeUserMissing 用户不存在（**不是**端口不可用：调用方按「主体不存在」处理）
var errFakeUserMissing = errors.New("fake principal: 用户不存在或已删除")

// errFakeGroupMissing 组不存在（同上）
var errFakeGroupMissing = errors.New("fake principal: 组不存在")

// errFakeUnavailable 端口不可用（模拟未接入用户体系；必须是 ErrFailClosed 族）
func errFakeUnavailable(method string) error {
	return errPrincipalUnavailable("fake." + method)
}

// UserExists 用户存在且未删
func (f *fakePrincipal) UserExists(_ context.Context, id uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return errFakeUnavailable("UserExists")
	}
	if _, ok := f.users[id]; !ok {
		return errFakeUserMissing
	}
	return nil
}

// UserActive 用户存在且启用
func (f *fakePrincipal) UserActive(_ context.Context, id uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return errFakeUnavailable("UserActive")
	}
	u, ok := f.users[id]
	if !ok {
		return errFakeUserMissing
	}
	if !u.Active {
		return errors.New("fake principal: 账号已停用")
	}
	return nil
}

// UserNames 批量用户名（缺 id 不返回）
func (f *fakePrincipal) UserNames(_ context.Context, ids []uint) (map[uint]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return nil, errFakeUnavailable("UserNames")
	}
	out := make(map[uint]string, len(ids))
	for _, id := range ids {
		if u, ok := f.users[id]; ok {
			out[id] = u.Name
		}
	}
	return out, nil
}

// EnabledGroupNames 启用组名集（主分组 + 附加组；组已删或禁用 ⇒ 不计入）
func (f *fakePrincipal) EnabledGroupNames(_ context.Context, id uint) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return nil, errFakeUnavailable("EnabledGroupNames")
	}
	names := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(groupID uint) {
		g, ok := f.groups[groupID]
		if !ok || !g.Enabled {
			return
		}
		if _, dup := seen[g.Name]; dup {
			return
		}
		seen[g.Name] = struct{}{}
		names = append(names, g.Name)
	}
	add(f.mainGroup[id])
	for _, gid := range f.extraGroups[id] {
		add(gid)
	}
	sort.Strings(names)
	return names, nil
}

// GroupNameIDs 组名 → 现行（最小 id）组 id；缺名不返回
func (f *fakePrincipal) GroupNameIDs(_ context.Context, names []string) (map[string]uint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return nil, errFakeUnavailable("GroupNameIDs")
	}
	ids := map[string]uint{}
	for _, name := range names {
		for id, g := range f.groups {
			if g.Name != name {
				continue
			}
			if cur, ok := ids[name]; !ok || id < cur {
				ids[name] = id
			}
		}
	}
	return ids, nil
}

// ResolveGroupID 组名 → 最小 id 组；不存在 ⇒ 错误
func (f *fakePrincipal) ResolveGroupID(_ context.Context, name string) (uint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return 0, errFakeUnavailable("ResolveGroupID")
	}
	var found uint
	for id, g := range f.groups {
		if g.Name != name {
			continue
		}
		if found == 0 || id < found {
			found = id
		}
	}
	if found == 0 {
		return 0, errFakeGroupMissing
	}
	return found, nil
}
