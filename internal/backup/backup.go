// Package backup 负责「一条命令备份、一条命令恢复」。
//
// 备份包（tar.gz）里装四样东西：
//
//	MANIFEST.json   —— 这是什么、什么时候、哪个版本、哪个 schema 版本、里面有什么
//	db.dump         —— pg_dump -Fc（自定义格式，恢复用 pg_restore）
//	config.toml     —— 配置（含各项开关；凭据类字段是加密存的，靠 secret.key 解）
//	secret.key      —— 数据目录里的加密密钥（**必须跟着走**，否则恢复后凭据解不开）
//	overlay/        —— 仅 --with-overlay：只读库的刮削产物（可选，体积大）
//
// 刻意**不**备份的东西：图片缓存、转码分片、探测工作目录 —— 它们都能重新生成。
// 备份要小、要快、要能一眼看懂里面有什么。
//
// 依赖：pg_dump / pg_restore（PostgreSQL 客户端工具）。找不到时给出人话错误 +
// 安装提示，而不是一句 "exec: not found"。可以用 [backup] 段显式指定路径。
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// FormatVersion 是备份包的格式版本。恢复时只认这一个版本 ——
// 见到不认识的版本直接拒绝，比猜着恢复安全。
const FormatVersion = 1

const (
	manifestName = "MANIFEST.json"
	dumpName     = "db.dump"
	configName   = "config.toml"
	secretName   = "secret.key"
	overlayDir   = "overlay"
)

// Manifest 是备份包的自述文件。
type Manifest struct {
	FormatVersion int       `json:"formatVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	LMBYVersion   string    `json:"lmbyVersion"`
	SchemaVersion int       `json:"schemaVersion"`
	DataDir       string    `json:"dataDir"`
	WithOverlay   bool      `json:"withOverlay"`
	Entries       []string  `json:"entries"`
}

// Options 是备份参数。
type Options struct {
	// Output 是输出的 tar.gz 路径；空则用当前目录下的 lmby-backup-<时间>.tar.gz。
	Output string
	// DSN 是源数据库。
	DSN string
	// DataDir 是数据目录（取 secret.key 与 overlay）。
	DataDir string
	// ConfigPath 是配置文件路径（通常 /etc/lmby/config.toml）；不存在就跳过。
	ConfigPath string
	// PgDump 是 pg_dump 可执行文件；空则从 PATH 找。
	PgDump string
	// WithOverlay 决定要不要把 overlay 一起打进去（体积大，默认不带）。
	WithOverlay bool
	// Version 是写入 MANIFEST 的版本号（由 cmd 层注入）。
	Version string
	Log     *slog.Logger
}

// Create 生成一个备份包，返回实际写入的路径。
func Create(ctx context.Context, o Options) (string, error) {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	pgDump, err := resolveTool(o.PgDump, "pg_dump",
		"没找到 pg_dump。它是 PostgreSQL 客户端工具：装一下 postgresql-client，或者用 [backup] pg_dump 指定路径")
	if err != nil {
		return "", err
	}

	out := o.Output
	if out == "" {
		out = fmt.Sprintf("lmby-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("解析输出路径失败: %w", err)
	}

	tmp, err := os.MkdirTemp("", "lmby-backup-*")
	if err != nil {
		return "", fmt.Errorf("建临时目录失败: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	// 1) 数据库
	dumpPath := filepath.Join(tmp, dumpName)
	log.Info("导出数据库", "tool", pgDump)
	cmd := exec.CommandContext(ctx, pgDump, "-Fc", "--no-owner", "--no-privileges", "-f", dumpPath, o.DSN)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("pg_dump 失败: %w（%s）", err, strings.TrimSpace(string(outBytes)))
	}

	schemaVersion, err := readSchemaVersion(ctx, o.DSN)
	if err != nil {
		// 拿不到 schema 版本不致命：备份照样能用，只是恢复时少一条提示。
		log.Warn("读取 schema 版本失败（备份继续，恢复时少一条提示）", "err", err)
	}

	// 2) 配置与密钥
	entries := []string{dumpName}
	if o.ConfigPath != "" {
		if _, err := os.Stat(o.ConfigPath); err == nil {
			if err := copyFile(o.ConfigPath, filepath.Join(tmp, configName)); err != nil {
				return "", fmt.Errorf("复制配置文件失败: %w", err)
			}
			entries = append(entries, configName)
		} else {
			log.Warn("配置文件不存在，跳过", "path", o.ConfigPath)
		}
	}
	if o.DataDir != "" {
		keyPath := filepath.Join(o.DataDir, secretName)
		if _, err := os.Stat(keyPath); err == nil {
			if err := copyFile(keyPath, filepath.Join(tmp, secretName)); err != nil {
				return "", fmt.Errorf("复制密钥失败: %w", err)
			}
			entries = append(entries, secretName)
		} else {
			log.Warn("数据目录里没有 secret.key（如果从没配过凭据，这是正常的）")
		}
	}

	// 3) 叠加层（可选）
	if o.WithOverlay && o.DataDir != "" {
		src := filepath.Join(o.DataDir, overlayDir)
		if _, err := os.Stat(src); err == nil {
			log.Info("打包叠加层（可能很大）", "from", src)
			if err := copyTree(src, filepath.Join(tmp, overlayDir)); err != nil {
				return "", fmt.Errorf("复制叠加层失败: %w", err)
			}
			entries = append(entries, overlayDir+"/")
		}
	}

	// 4) 自述文件
	m := Manifest{
		FormatVersion: FormatVersion,
		CreatedAt:     time.Now().UTC(),
		LMBYVersion:   o.Version,
		SchemaVersion: schemaVersion,
		DataDir:       o.DataDir,
		WithOverlay:   o.WithOverlay,
		Entries:       entries,
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("生成 MANIFEST 失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, manifestName), mb, 0o600); err != nil {
		return "", fmt.Errorf("写 MANIFEST 失败: %w", err)
	}

	// 5) 打包
	if err := writeTarGz(absOut, tmp); err != nil {
		return "", err
	}
	log.Info("备份完成", "path", absOut, "schemaVersion", schemaVersion, "withOverlay", o.WithOverlay)
	return absOut, nil
}

// RestoreOptions 是恢复参数。
type RestoreOptions struct {
	// Input 是备份包路径。
	Input string
	// DSN 是目标数据库（可与备份时不同：恢复到新实例就用得上）。
	DSN string
	// DataDir 是目标数据目录（写 secret.key 与 overlay）。
	DataDir string
	// ConfigPath 是目标配置文件路径。
	ConfigPath string
	// PgRestore 是 pg_restore 可执行文件；空则从 PATH 找。
	PgRestore string
	// Confirm 必须为 true —— 恢复会**清空并覆盖**目标库，这是不可逆的。
	Confirm bool
	// OverwriteConfig 决定要不要覆盖已存在的 config.toml / secret.key（默认不覆盖）。
	OverwriteConfig bool
	Log             *slog.Logger
}

// Restore 用备份包覆盖目标实例。
//
// 破坏性：会 `pg_restore --clean` 目标库（先删对象再建），所以必须 Confirm。
func Restore(ctx context.Context, o RestoreOptions) error {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if !o.Confirm {
		return errors.New("恢复会清空并覆盖目标数据库，这是不可逆的：确认后请加 --yes")
	}
	pgRestore, err := resolveTool(o.PgRestore, "pg_restore",
		"没找到 pg_restore。它是 PostgreSQL 客户端工具：装一下 postgresql-client，或者用 [backup] pg_restore 指定路径")
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "lmby-restore-*")
	if err != nil {
		return fmt.Errorf("建临时目录失败: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := readTarGz(o.Input, tmp); err != nil {
		return err
	}
	m, err := readManifest(filepath.Join(tmp, manifestName))
	if err != nil {
		return err
	}
	if m.FormatVersion != FormatVersion {
		return fmt.Errorf("这是格式版本 %d 的备份包，本版本只认 %d", m.FormatVersion, FormatVersion)
	}
	log.Info("准备恢复", "createdAt", m.CreatedAt.Format(time.RFC3339), "schemaVersion", m.SchemaVersion,
		"withOverlay", m.WithOverlay, "entries", strings.Join(m.Entries, ", "))

	// 1) 数据库（--clean：先删对象，避免「表已存在」的半吊子状态）
	dumpPath := filepath.Join(tmp, dumpName)
	if _, err := os.Stat(dumpPath); err != nil {
		return fmt.Errorf("备份包里没有 %s，无法恢复数据库", dumpName)
	}
	log.Warn("恢复数据库：会清空目标库里的现有数据", "dsn", maskDSN(o.DSN))
	cmd := exec.CommandContext(ctx, pgRestore, "--clean", "--if-exists", "--no-owner", "--no-privileges",
		"-d", o.DSN, dumpPath)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_restore 失败: %w（%s）", err, strings.TrimSpace(string(outBytes)))
	}

	// 2) 密钥：**先写** —— config.toml 里的凭据要靠它解开
	if o.DataDir != "" {
		if err := installFile(filepath.Join(tmp, secretName), filepath.Join(o.DataDir, secretName),
			o.OverwriteConfig, 0o600, log); err != nil {
			return err
		}
	}
	// 3) 配置
	if o.ConfigPath != "" {
		if err := installFile(filepath.Join(tmp, configName), o.ConfigPath,
			o.OverwriteConfig, 0o600, log); err != nil {
			return err
		}
	}
	// 4) 叠加层（有就解出来；已有文件会被覆盖，因为备份里的才是「这份数据」的样子）
	if o.DataDir != "" {
		src := filepath.Join(tmp, overlayDir)
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(o.DataDir, overlayDir)
			if err := copyTree(src, dst); err != nil {
				return fmt.Errorf("恢复叠加层失败: %w", err)
			}
			log.Info("叠加层已恢复", "to", dst)
		}
	}
	log.Info("恢复完成 —— 重启服务后生效（systemctl restart lmby）")
	return nil
}

// ---- 内部工具 ----

func resolveTool(explicit, name, hint string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("配置里指定的 %s 不存在：%s", name, explicit)
		}
		return explicit, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", errors.New(hint)
	}
	return p, nil
}

func readManifest(path string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("这个文件不像 LMBY 的备份包（读不到 %s）", manifestName)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s 解析失败: %w", manifestName, err)
	}
	return m, nil
}

func readSchemaVersion(ctx context.Context, dsn string) (int, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close(ctx) }()
	var v int
	if err := conn.QueryRow(ctx, `select coalesce(max(version), 0) from schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// installFile 把一个文件装到目标位置：目标已存在且不允许覆盖时**不动**（并说清楚），
// 这样「恢复数据」不会顺手把现有实例的配置/密钥换掉。
func installFile(src, dst string, overwrite bool, mode os.FileMode, log *slog.Logger) error {
	if _, err := os.Stat(src); err != nil {
		return nil // 备份包里没有这一项（比如从没配过凭据）
	}
	if _, err := os.Stat(dst); err == nil && !overwrite {
		log.Warn("目标已存在，未覆盖（要覆盖请加 --overwrite-config）", "path", dst)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("建目录失败: %w", err)
	}
	return copyFileMode(src, dst, mode)
}

func copyFile(src, dst string) error { return copyFileMode(src, dst, 0o600) }

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileMode(path, target, 0o644)
	})
}

func writeTarGz(dest, dir string) error {
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("创建备份文件失败: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		_, err = io.Copy(tw, in)
		return err
	})
	if err != nil {
		return fmt.Errorf("打包失败: %w", err)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Sync()
}

func readTarGz(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开备份包失败: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("这不是一个 gzip 文件: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("读取备份包失败: %w", err)
		}
		// 防目录穿越：包里的路径必须落在临时目录内。
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("备份包里有可疑路径，已拒绝：%s", hdr.Name)
		}
		target := filepath.Join(dir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

// maskDSN 把连接串里的口令换成 ***，免得它出现在日志或终端里。
func maskDSN(dsn string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			if colon := strings.Index(rest[:at], ":"); colon >= 0 {
				return dsn[:i+3] + rest[:colon+1] + "***" + rest[at:]
			}
		}
	}
	return dsn
}
