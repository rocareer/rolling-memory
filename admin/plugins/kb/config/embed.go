// plugin: kb v1.0.0
// Package kbconfig 内嵌本插件的**缺省配置**（config/kb.yaml）。
//
// 为什么单独成包：go:embed 只能嵌入**同目录或子目录**的文件，而缺省配置按目录纪律落在
// 插件根的 config/（与宿主 config/ 无关、随插件安装落地、运维可读）。本包只做一件事：
// 把那份 YAML 变成 []byte 交给 kbcfg 解析——不放任何取值逻辑（取值单点在 kbcfg）。
package kbconfig

import _ "embed"

// DefaultYAML 本插件的缺省配置（config/kb.yaml 逐字节嵌入；解析与覆盖单点在 kbcfg）
//
//go:embed kb.yaml
var DefaultYAML []byte
