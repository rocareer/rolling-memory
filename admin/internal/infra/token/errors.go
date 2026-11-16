package token

import (
	"errors"
	"fmt"
)

// 令牌错误契约：
//
//   - 查无此行不是错误：Service.Get 对不存在的令牌返回 (nil, nil)，与驱动层把
//     gorm.ErrRecordNotFound 归一为「不存在」的约定一致。认证方据此下发 401（重新登录）；
//   - 基础设施故障必须可判类型：认证路径（Service.Get）的数据库连接失败、SQL 执行错误等
//     包装为 *StorageError，认证方用 IsStorageError 判定后下发 503，且不得携带 need_login——
//     一旦把瞬时故障当成认证失败，客户端会清掉本地会话，全部在线账号被强制登出；
//   - 令牌类型非闭集等属调用方编程错误，按普通 error 返回，不归入存储故障。
//
// 判定入口（可独立单测，不触库）：IsStorageError。

// StorageError 令牌存储层故障（基础设施错误）。
// Op 记录出错的操作名（当前仅认证读取路径使用 get），Err 为底层原因，
// 可用 errors.Is / errors.As 穿透判定。
type StorageError struct {
	Op  string
	Err error
}

// Error 实现 error 接口
func (e *StorageError) Error() string {
	if e == nil {
		return "令牌存储不可用"
	}
	if e.Err == nil {
		return fmt.Sprintf("令牌存储不可用（%s）", e.Op)
	}
	return fmt.Sprintf("令牌存储不可用（%s）: %v", e.Op, e.Err)
}

// Unwrap 支持 errors.Is / errors.As 穿透到底层原因
func (e *StorageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsStorageError 判断错误是否属于令牌存储层故障（数据库 / 驱动不可用）。
// 认证中间件据此把「基础设施故障」（503，保留客户端会话）与「令牌失效」（401，清会话重登）分开。
func IsStorageError(err error) bool {
	var se *StorageError
	return errors.As(err, &se)
}

// storageError 把操作失败包装为 *StorageError；err 为 nil 时返回 nil，便于直接 return。
func storageError(op string, err error) error {
	if err == nil {
		return nil
	}
	return &StorageError{Op: op, Err: err}
}
