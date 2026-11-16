// plugin: kb v1.0.0
//go:build !unix

// kb 查询向量缓存跨进程锁（非 unix 面：无 flock——退化为进程内串行）。
//
// 锁实现形态照既有最小实现（多实例安全口径）。
//
// 平台事实：Windows 等无 flock(2) 等价物（LockFileEx 语义不同且本包不愿引平台分支锁）。
// 本包内调用已由 queryVecCache.mu 串行，故此处返回空释放函数（不报错）——单进程内正确、
// 文件仍唯一临时名 + 原子改名（不损），但**多进程共享同一文件时仍可能丢键**（如实）。
// 该场景的硬隔离手段 = 显式独立文件（config kb.search.query_cache_file，每实例一个路径）。
package search

// lockQueryCacheFile 非 unix 退化：恒成功、无跨进程互斥（见文件头注）。
func lockQueryCacheFile(path string) (func(), error) {
	return func() {}, nil
}
