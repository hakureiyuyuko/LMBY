package backup

// 备份包本身的规则用单测钉住（不碰数据库 —— pg_dump 那条路归验收脚本）。
//
// 这里最要紧的一条是**目录穿越防护**：备份包是文件，可能是别人给的 ——
// 解包时如果照单全收地写 `../../etc/...`，那就是一个「恢复备份」变成本地提权的洞。

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAndReadTarGzRoundTrip(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "overlay", "1", "2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, dumpName), []byte("dump-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "overlay", "1", "2", "poster.jpg"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}

	pack := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := writeTarGz(pack, src); err != nil {
		t.Fatalf("writeTarGz: %v", err)
	}
	if err := readTarGz(pack, dst); err != nil {
		t.Fatalf("readTarGz: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, dumpName))
	if err != nil || string(got) != "dump-bytes" {
		t.Fatalf("db.dump 往返失败：%q %v", got, err)
	}
	img, err := os.ReadFile(filepath.Join(dst, "overlay", "1", "2", "poster.jpg"))
	if err != nil || string(img) != "img" {
		t.Fatalf("overlay 往返失败：%q %v", img, err)
	}
}

// readTarGz 必须拒绝包里指向外部的路径 —— 备份包可能是别人给的。
func TestReadTarGzRejectsPathTraversal(t *testing.T) {
	pack := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, err := os.Create(pack)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{
		Name: "../escaped.txt", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	err = readTarGz(pack, t.TempDir())
	if err == nil {
		t.Fatal("含 ../ 的备份包应该被拒绝，却通过了")
	}
	if !strings.Contains(err.Error(), "可疑路径") {
		t.Fatalf("拒绝理由要能看懂，实际：%v", err)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Manifest{
		FormatVersion: FormatVersion,
		CreatedAt:     time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
		LMBYVersion:   "v0.9.0-dev",
		SchemaVersion: 18,
		DataDir:       "/var/lib/lmby",
		WithOverlay:   true,
		Entries:       []string{dumpName, configName, secretName, "overlay/"},
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readManifest(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != want.SchemaVersion || got.LMBYVersion != want.LMBYVersion || !got.WithOverlay {
		t.Fatalf("MANIFEST 往返丢字段：%+v", got)
	}
}

func TestReadManifestOnGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(filepath.Join(dir, manifestName)); err == nil {
		t.Fatal("非 JSON 的 MANIFEST 应该报错")
	}
	if _, err := readManifest(filepath.Join(dir, "nope.json")); err == nil {
		t.Fatal("缺 MANIFEST 应该给出「这不像备份包」的提示")
	}
}

func TestMaskDSN(t *testing.T) {
	got := maskDSN("postgres://lmby:s3cret@127.0.0.1:5432/lmby?sslmode=disable")
	if strings.Contains(got, "s3cret") {
		t.Fatalf("口令不能出现在日志里：%s", got)
	}
	if !strings.Contains(got, "lmby:***@") {
		t.Fatalf("遮罩后的形状不对：%s", got)
	}
	if maskDSN("host=127.0.0.1 dbname=lmby") != "host=127.0.0.1 dbname=lmby" {
		t.Fatal("不是 URL 形式的 DSN 应原样返回")
	}
}

func TestResolveToolExplicitMissing(t *testing.T) {
	if _, err := resolveTool("/definitely/not/here/pg_dump", "pg_dump", "hint"); err == nil {
		t.Fatal("显式指定不存在的工具应该报错")
	}
}
