package host

import (
	"fmt"
	"testing"

	"gorm.io/datatypes"
)

func specEntity(name, spec string) Entity {
	return Entity{Plugin: "demo", Name: name, Spec: datatypes.JSON([]byte(spec))}
}

func TestValidateSpec(t *testing.T) {
	cases := []struct {
		what string
		name string
		spec string
	}{
		// 合法：最小形态
		{"最小合法 spec", "items", `{"entity":"items","fields":[{"name":"title","type":"string"},{"name":"status","type":"int"}]}`},
		// 合法：全键全值域（保证闭集不误拒，键集）
		{"全键合法 spec", "items", `{"entity":"items","title_zh":"条目","title_en":"Items","drag_sort":true,"ddl_owner":"engine",` +
			`"fields":[{"name":"title","type":"string","title_zh":"标题","title_en":"Title","required":true,"length":128,` +
			`"default":"","unique":false,"index":false,"list":true,"sort":true,"search":{"op":"LIKE"},"quickSearch":true,` +
			`"form":"input","dict":[{"value":1,"label_zh":"启用","label_en":"Enabled"}],"trgm":false},` +
			`{"name":"status","type":"enum","form":"radio","search":{"op":"NOT ILIKE"},` +
			`"dict":[{"value":"on","label_zh":"开","label_en":"On"}]}]}`},
		// 非法：标识符
		{"实体名非法", "Items", `{"entity":"Items","fields":[{"name":"title","type":"string"}]}`},
		{"字段名非法", "items", `{"entity":"items","fields":[{"name":"Bad-Name","type":"string"}]}`},
		// 非法：JSON / 必备键
		{"JSON 非法", "items", `{"entity":"items",`},
		{"JSON 非对象", "items", `["items"]`},
		{"entity 缺失", "items", `{"fields":[{"name":"title","type":"string"}]}`},
		{"entity 与行名不一致", "items", `{"entity":"other","fields":[{"name":"title","type":"string"}]}`},
		{"fields 为空", "items", `{"entity":"items","fields":[]}`},
		{"缺 fields", "items", `{"entity":"items"}`},
		{"缺 name", "items", `{"entity":"items","fields":[{"type":"string"}]}`},
		{"缺 type", "items", `{"entity":"items","fields":[{"name":"title"}]}`},
		// 非法：闭集键（多余键一律拒装）
		{"实体级未知键", "items", `{"entity":"items","foo":1,"fields":[{"name":"title","type":"string"}]}`},
		{"字段级未知键", "items", `{"entity":"items","fields":[{"name":"title","type":"string","foo":1}]}`},
		{"search 未知子键", "items", `{"entity":"items","fields":[{"name":"title","type":"string","search":{"op":"LIKE","x":1}}]}`},
		{"dict 条目未知键", "items", `{"entity":"items","fields":[{"name":"title","type":"enum","dict":[{"value":1,"label_zh":"启用","label_en":"On","color":"green"}]}]}`},
		// 非法：值域闭集
		{"类型不在闭集", "items", `{"entity":"items","fields":[{"name":"title","type":"json"}]}`},
		{"form 不在闭集", "items", `{"entity":"items","fields":[{"name":"title","type":"string","form":"upload"}]}`},
		{"ddl_owner 不在闭集", "items", `{"entity":"items","ddl_owner":"auto","fields":[{"name":"title","type":"string"}]}`},
		{"操作符词表外（gte）", "items", `{"entity":"items","fields":[{"name":"title","type":"string","search":{"op":"gte"}}]}`},
		{"操作符词表外（like 小写）", "items", `{"entity":"items","fields":[{"name":"title","type":"string","search":{"op":"like"}}]}`},
		{"search 非对象", "items", `{"entity":"items","fields":[{"name":"title","type":"string","search":"LIKE"}]}`},
		// 非法：dict 形状
		{"dict 非数组", "items", `{"entity":"items","fields":[{"name":"title","type":"enum","dict":{"1":"启用"}}]}`},
		{"dict 空数组", "items", `{"entity":"items","fields":[{"name":"title","type":"enum","dict":[]}]}`},
		{"dict 缺 label_en", "items", `{"entity":"items","fields":[{"name":"title","type":"enum","dict":[{"value":1,"label_zh":"启用"}]}]}`},
		{"dict.value 类型非法", "items", `{"entity":"items","fields":[{"name":"title","type":"enum","dict":[{"value":[1],"label_zh":"启用","label_en":"On"}]}]}`},
		// 非法：键值**类型**（键集闭集只查键名；这些键的值域是数字/布尔/字符串，类型错必须拒）
		{"drag_sort 非布尔", "items", `{"entity":"items","drag_sort":"yes","fields":[{"name":"title","type":"string"}]}`},
		{"drag_sort 数字", "items", `{"entity":"items","drag_sort":1,"fields":[{"name":"title","type":"string"}]}`},
		{"实体 title_zh 非字符串", "items", `{"entity":"items","title_zh":128,"fields":[{"name":"title","type":"string"}]}`},
		{"字段 title_en 非字符串", "items", `{"entity":"items","fields":[{"name":"title","type":"string","title_en":1}]}`},
		{"required 非布尔", "items", `{"entity":"items","fields":[{"name":"title","type":"string","required":"true"}]}`},
		{"unique 非布尔", "items", `{"entity":"items","fields":[{"name":"title","type":"string","unique":"yes"}]}`},
		{"index 非布尔", "items", `{"entity":"items","fields":[{"name":"title","type":"string","index":1}]}`},
		{"list 非布尔", "items", `{"entity":"items","fields":[{"name":"title","type":"string","list":"true"}]}`},
		{"sort 非布尔", "items", `{"entity":"items","fields":[{"name":"title","type":"string","sort":"false"}]}`},
		{"quickSearch 非布尔", "items", `{"entity":"items","fields":[{"name":"title","type":"string","quickSearch":"on"}]}`},
		{"trgm 非布尔", "items", `{"entity":"items","fields":[{"name":"title","type":"string","trgm":"no"}]}`},
		{"length 非数字", "items", `{"entity":"items","fields":[{"name":"title","type":"string","length":"abc"}]}`},
		{"length 字符串数字", "items", `{"entity":"items","fields":[{"name":"title","type":"string","length":"128"}]}`},
		{"length 小数", "items", `{"entity":"items","fields":[{"name":"title","type":"string","length":1.5}]}`},
		{"length 负数", "items", `{"entity":"items","fields":[{"name":"title","type":"string","length":-1}]}`},
		{"length 布尔", "items", `{"entity":"items","fields":[{"name":"title","type":"string","length":true}]}`},
		{"length null", "items", `{"entity":"items","fields":[{"name":"title","type":"string","length":null}]}`},
		{"drag_sort null", "items", `{"entity":"items","drag_sort":null,"fields":[{"name":"title","type":"string"}]}`},
		{"required null", "items", `{"entity":"items","fields":[{"name":"title","type":"string","required":null}]}`},
		{"实体 title_zh null", "items", `{"entity":"items","title_zh":null,"fields":[{"name":"title","type":"string"}]}`},
		// 非法：重复字段名
		{"字段名重复", "items", `{"entity":"items","fields":[{"name":"title","type":"string"},{"name":"title","type":"text"}]}`},
	}
	for _, c := range cases {
		err := ValidateSpec(specEntity(c.name, c.spec))
		wantOK := c.what == "最小合法 spec" || c.what == "全键合法 spec"
		if wantOK && err != nil {
			t.Errorf("%s: 合法 spec 被拒: %v", c.what, err)
		}
		if !wantOK && err == nil {
			t.Errorf("%s: 应报错（fail-closed）", c.what)
		}
	}
}

// TestSetSpecValidator 追加语义（L2 引擎的增补规则经它同时服务安装期与启动预检）：
// 注册的校验器在**内置闭集校验之后**执行——命中即拒，但永远拿不掉宿主自己的闭集校验。
func TestSetSpecValidator(t *testing.T) {
	defer SetSpecValidator(nil)

	// 只放行 name == "items" 的追加校验器：用来证明「内置校验先行」（坏 spec 到不了追加校验器）
	calls := 0
	SetSpecValidator(func(entity Entity) error {
		calls++
		if entity.Name != "items" {
			return fmt.Errorf("追加校验器被调用：name=%q", entity.Name)
		}
		return nil
	})

	legal := `{"entity":"items","fields":[{"name":"title","type":"string"}]}`
	if err := ValidateSpec(specEntity("items", legal)); err != nil {
		t.Fatalf("合法 spec 应通过（内置 + 追加都放行）: %v", err)
	}
	if calls != 1 {
		t.Fatalf("追加校验器调用次数 = %d, want 1", calls)
	}

	// 内置闭集校验不可被追加校验器绕过：词表外的操作符必须仍被拒，且追加校验器不应被调用
	before := calls
	if err := ValidateSpec(specEntity("items", `{"entity":"items","fields":[{"name":"title","type":"string","search":{"op":"gte"}}]}`)); err == nil {
		t.Error("内置闭集校验不得被追加校验器绕过：词表外的操作符应被拒")
	}
	if calls != before {
		t.Errorf("内置校验失败时不应再调用追加校验器，调用次数由 %d 变为 %d", before, calls)
	}

	// 非法的实体名同样由内置校验拦下（追加校验器看不到它）
	if err := ValidateSpec(Entity{Name: "Any-Name"}); err == nil {
		t.Error("内置校验（标识符形状）必须先行生效")
	}

	// 传 nil 清空追加校验器：内置闭集校验继续生效
	SetSpecValidator(nil)
	if err := ValidateSpec(specEntity("items", legal)); err != nil {
		t.Fatalf("清空追加校验器后合法 spec 仍应通过: %v", err)
	}
	if err := ValidateSpec(specEntity("items", `{"entity":"items","fields":[{"name":"title","type":"string","search":{"op":"gte"}}]}`)); err == nil {
		t.Error("清空追加校验器后，词表外的操作符应仍被内置校验拒")
	}
}
