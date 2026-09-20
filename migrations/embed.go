// Package migrations 内嵌所有 SQL 迁移文件。
//
// 迁移文件命名规则：<四位版本号>_<描述>.sql，例如 0001_init.sql。
// 版本号必须唯一且递增；文件一旦发布不可修改（校验和会被记录）。
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var fsys embed.FS

// Migration 是一个已解析的迁移文件。
type Migration struct {
	Version int64  // 版本号，取自文件名前缀
	Name    string // 描述，取自文件名（去掉版本号与扩展名）
	SQL     string // 文件全文
}

// All 返回按版本号升序排列的全部迁移。
func All() ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}

	var out []Migration
	seen := map[int64]string{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".sql")
		verStr, desc, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("迁移文件名缺少下划线分隔的版本前缀: %s", e.Name())
		}
		ver, err := strconv.ParseInt(verStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("迁移文件名版本号非法 (%s): %w", e.Name(), err)
		}
		if ver <= 0 {
			return nil, fmt.Errorf("迁移文件名版本号必须为正数: %s", e.Name())
		}
		if prev, dup := seen[ver]; dup {
			return nil, fmt.Errorf("迁移版本号重复: %d（%s 与 %s）", ver, prev, e.Name())
		}
		seen[ver] = e.Name()

		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: ver, Name: desc, SQL: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
