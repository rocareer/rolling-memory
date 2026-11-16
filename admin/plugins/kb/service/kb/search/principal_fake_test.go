// plugin: kb v1.0.0
// 测试本地假体（search 包）：user 轴主体面端口的内存实现。
//
// 为什么本包自带一份：本插件不假设宿主有用户体系（user 轴经端口注入），而端口实现必须由**注入方**
// 提供——service/kb 包内的假体是它自己的测试件（`_test.go` 不可跨包导入），故本包按需持一份最小实现。
// 这里只覆盖正向布景（组/成员/用户名），fail-closed 面向的断言在 service/kb 包的用例里。
package search

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// kkGroup 假体组
type kkGroup struct {
	ID      uint
	Name    string
	Enabled bool
}

// kkUser 假体用户
type kkUser struct {
	ID     uint
	Name   string
	Active bool
}

// kkPrincipal Principal 端口的内部测试实现
type kkPrincipal struct {
	mu sync.Mutex

	nextUser  uint
	nextGroup uint

	users       map[uint]*kkUser
	groups      map[uint]*kkGroup
	mainGroup   map[uint]uint
	extraGroups map[uint][]uint
}

// newKKPrincipal 新建假体
func newKKPrincipal() *kkPrincipal {
	return &kkPrincipal{
		users:       map[uint]*kkUser{},
		groups:      map[uint]*kkGroup{},
		mainGroup:   map[uint]uint{},
		extraGroups: map[uint][]uint{},
	}
}

// AddGroup 建组
func (f *kkPrincipal) AddGroup(name string, enabled bool) *kkGroup {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextGroup++
	g := &kkGroup{ID: f.nextGroup, Name: name, Enabled: enabled}
	f.groups[g.ID] = g
	return g
}

// AddUser 建用户（mainGroupID=0 = 无主分组）
func (f *kkPrincipal) AddUser(name string, active bool, mainGroupID uint) *kkUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextUser++
	u := &kkUser{ID: f.nextUser, Name: name, Active: active}
	f.users[u.ID] = u
	if mainGroupID > 0 {
		f.mainGroup[u.ID] = mainGroupID
	}
	return u
}

// UserExists 用户存在
func (f *kkPrincipal) UserExists(_ context.Context, id uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[id]; !ok {
		return errors.New("fake principal: 用户不存在或已删除")
	}
	return nil
}

// UserActive 用户存在且启用
func (f *kkPrincipal) UserActive(_ context.Context, id uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return errors.New("fake principal: 用户不存在或已删除")
	}
	if !u.Active {
		return errors.New("fake principal: 账号已停用")
	}
	return nil
}

// UserNames 批量用户名
func (f *kkPrincipal) UserNames(_ context.Context, ids []uint) (map[uint]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[uint]string, len(ids))
	for _, id := range ids {
		if u, ok := f.users[id]; ok {
			out[id] = u.Name
		}
	}
	return out, nil
}

// EnabledGroupNames 启用组名集（主分组 + 附加组；组已删/禁用不计入）
func (f *kkPrincipal) EnabledGroupNames(_ context.Context, id uint) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

// GroupNameIDs 组名 → 最小 id 组
func (f *kkPrincipal) GroupNameIDs(_ context.Context, names []string) (map[string]uint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

// ResolveGroupID 组名 → 最小 id 组
func (f *kkPrincipal) ResolveGroupID(_ context.Context, name string) (uint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
		return 0, errors.New("fake principal: 组不存在")
	}
	return found, nil
}
