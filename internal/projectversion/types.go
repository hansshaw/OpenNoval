// Package projectversion stores independent, verified versions of a project's
// durable files. Callers must hold the project lease and exclude concurrent
// writes for Create, Compare and Restore. Restore requires reopening any stores
// which cache project data after it returns.
package projectversion

import "errors"

var (
	ErrUnsafeFile      = errors.New("项目含链接或非普通文件，无法安全保存或恢复版本")
	ErrInvalidID       = errors.New("版本标识无效")
	ErrNotFound        = errors.New("版本不存在")
	ErrChanged         = errors.New("项目已变化，请重新预览差异后恢复")
	ErrCorrupt         = errors.New("版本文件损坏或校验失败")
	ErrRecoveryPending = errors.New("项目有未完成的版本恢复，请先重新打开项目完成恢复")
)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	CreatedAt   string `json:"createdAt"`
	Fingerprint string `json:"fingerprint"`
	Files       []File `json:"files"`
}

// Changes describe the operation restoring the snapshot would perform on the
// current project: added means present only in the snapshot; deleted means
// present only in the current project. Text diffs use current -> snapshot.
type Change struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	OldSize   int64  `json:"oldSize"`
	NewSize   int64  `json:"newSize"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type Comparison struct {
	Snapshot    Manifest `json:"snapshot"`
	Fingerprint string   `json:"fingerprint"`
	Changes     []Change `json:"changes"`
	Truncated   bool     `json:"truncated"`
}

type RestoreResult struct {
	Backup   Manifest `json:"backup"`
	Restored Manifest `json:"restored"`
}
