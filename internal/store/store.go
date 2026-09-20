// Package store 负责 PostgreSQL 连接、迁移与数据访问。
package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 封装 PostgreSQL 连接池与各领域仓储方法。
type Store struct {
	pool *pgxpool.Pool
}

// Open 建立连接池并做一次 Ping 验证。
func Open(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析数据库 DSN 失败: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if err := checkEncoding(pingCtx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// checkEncoding 确认数据库的编码与字符类都是 UTF-8，否则直接拒绝启动。
//
// 两件事都要查，而**且都要 UTF-8**：
//   - `server_encoding`：不是 UTF8 时，所有字符语义退化成字节语义
//     （length('钢') = 3、to_tsvector 认不出中文）；
//   - `datctype`（lc_ctype）：编码是 UTF8 但 ctype 是 C 时，编码没问题，
//     但 pg_trgm 切不出中文三元组（show_trgm('中文') 是空的），
//     「中文模糊匹配 / 错字容忍」这一整路会静默失效。
//
// 为什么要在启动时硬拦：这两件事都不会报错，只会在搜索/排序的某一刻给出错误结果。
// 最常见的来路：宿主 locale 是 C（最小化系统默认就是），`createdb` 建出来的库
// 就是 SQL_ASCII + C；只补一个 `-E UTF8` 还会剩下 C 的 ctype。
// Docker 官方镜像不会有这个问题（initdb 用 en_US.utf8）。
func checkEncoding(ctx context.Context, pool *pgxpool.Pool) error {
	var enc, ctype string
	if err := pool.QueryRow(ctx,
		`select current_setting('server_encoding'),
		        (select datctype from pg_database where datname = current_database())`,
	).Scan(&enc, &ctype); err != nil {
		return fmt.Errorf("读取数据库编码信息失败: %w", err)
	}
	encOK := strings.EqualFold(enc, "UTF8")
	// C.UTF-8 / en_US.utf8 / zh_CN.utf8 都算；C / POSIX 不算
	ctypeOK := strings.Contains(strings.ToLower(ctype), "utf")
	if encOK && ctypeOK {
		return nil
	}
	return fmt.Errorf(
		"数据库字符集不满足要求：encoding=%s、lc_ctype=%s（需要 UTF8 + 一个 UTF-8 的 ctype）。\n"+
			"  编码不是 UTF8 时中文会退化成字节（length('钢')=3、分词失效）；\n"+
			"  lc_ctype 是 C 时 pg_trgm 切不出中文三元组（模糊匹配与错字容忍会静默失效）。\n"+
			"修法：停掉服务后跑 scripts/dev/fix-db-encoding.sh（dump → 旧库改名保留 → "+
			"用 UTF8 + C.UTF-8 重建 → 还原 → 逐表比对行数并做中文三元组自检）", enc, ctype)
}

// Pool 暴露底层连接池（供尚未封装的查询使用）。
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close 关闭连接池。
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Ping 检查数据库连通性。
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
