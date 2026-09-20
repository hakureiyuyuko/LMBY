package api

import "os"

// statPath 检查路径是否存在，用于建库时的可达性校验。
//
// 单独抽成函数是为了留一个可替换点：将来的测试与「路径映射」功能
// 都可能需要换掉真实的 os.Stat。
var statPath = os.Stat
