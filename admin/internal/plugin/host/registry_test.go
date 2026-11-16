package host

import "testing"

// 用例统一用本前缀命名，避免与其它用例（及真实插件登记）互相污染
const registryTestPrefix = "testreg-"

func TestRegisterLookupRegistered(t *testing.T) {
	Register(Meta{Name: registryTestPrefix + "b", Version: "1.0.0", Kind: "code"})
	Register(Meta{Name: registryTestPrefix + "a", Version: "2.1.0", Kind: "mixed"})

	got, ok := Lookup(registryTestPrefix + "b")
	if !ok || got.Version != "1.0.0" || got.Kind != "code" {
		t.Fatalf("Lookup(%sb) = %+v, %v", registryTestPrefix, got, ok)
	}

	if _, ok := Lookup(registryTestPrefix + "absent"); ok {
		t.Error("未登记的名字不应命中")
	}

	// 插件名首尾空白在登记时归一
	Register(Meta{Name: " " + registryTestPrefix + "c" + " "})
	if _, ok := Lookup(registryTestPrefix + "c"); !ok {
		t.Error("Register 应去除插件名首尾空白")
	}

	// Registered 按 name 升序，且包含本用例登记的行
	all := Registered()
	seen := make(map[string]Meta, len(all))
	for i := range all {
		seen[all[i].Name] = all[i]
		if i > 0 && all[i-1].Name >= all[i].Name {
			t.Fatalf("Registered 顺序应为 name 升序: %q 在 %q 之前", all[i].Name, all[i-1].Name)
		}
	}
	for _, name := range []string{registryTestPrefix + "a", registryTestPrefix + "b", registryTestPrefix + "c"} {
		if _, ok := seen[name]; !ok {
			t.Errorf("Registered 缺少已登记插件 %s", name)
		}
	}
}

func TestRegisterPanics(t *testing.T) {
	assertPanic(t, "空名登记", func() { Register(Meta{Name: "   "}) })

	Register(Meta{Name: registryTestPrefix + "dup"})
	assertPanic(t, "重复名登记", func() { Register(Meta{Name: registryTestPrefix + "dup"}) })
}

func TestGatePanicsOnEmptyName(t *testing.T) {
	assertPanic(t, "空名建闸", func() { Gate("  ") })
}

func assertPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s 未 panic", what)
		}
	}()
	fn()
}
