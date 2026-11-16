// plugin: kb v1.0.0
//go:build unix

// kb 查询向量缓存跨进程锁（unix 面：flock LOCK_EX）。
//
// 最小实现照既有形态（不跨包引用其它模块内部——其非导出符号不可用）；本文件落本插件侧
// 同型缺陷修复，语义与先例逐条一致：
//
//   - 锁文件 = 数据文件同路径 + ".lock"（同目录，权限 0644）——所有共享同一数据文件的进程取同一把锁，
//     写入序列变为「加锁 → 重读合并 → 原子替换 → 解锁」，互相不覆盖；
//   - flock 语义：锁随打开文件描述（open file description）独立——同进程内两个实例（两次 open）亦互斥，
//     进程崩溃即释放（无残留锁需回收）。
package search

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockQueryCacheFile 取得跨进程排他锁（阻塞直到可用）；返回释放函数（解锁 + 关 fd）。
// 失败 = 返回错误（调用方降级为无锁写入并提示一次；不阻断缓存写入）。
func lockQueryCacheFile(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
