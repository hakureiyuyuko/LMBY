// Package version 保存编译期注入的版本信息。
package version

import "fmt"

// 这些变量通过 -ldflags "-X ..." 在构建时注入。
var (
	Version   = "dev"
	Commit    = "none"
	BuildTime = "unknown"
)

// String 返回人类可读的版本描述。
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, BuildTime)
}
