// Package overlay 是「只读媒体库」的写入层。
//
// 背景：库可以挂在网盘 / 只读挂载上（Alist、rclone、NFS ro、只读 bind mount），
// 这种库上 LMBY **一个字节也不能往库目录里写**。但刮削仍然要产出东西
// （图片、元数据快照），所以需要一个「不落在媒体目录里」的落点 ——
// 就是一个叠加层（overlay），放在数据目录下，按库分块：
//
//	<数据目录>/overlay/<libraryId>/<itemId>/poster.jpg
//	<数据目录>/overlay/<libraryId>/<itemId>/meta.json
//
// 三条设计取舍：
//
//  1. **库级目录，不是路径级**：overlay 里的东西属于「这个库」，与挂载点变没变无关；
//     条目换路径（重新挂载换了挂载点）不会让叠加层的东西对不上号。
//  2. **不参与图片缓存淘汰**：`images` 包里的 cache/ 是会按上限清理的缓存，
//     而 overlay 里放的是「这个库的刮削产物」，清掉了就得重新联网刮一次 ——
//     所以它算数据，不算缓存。
//  3. **写入前有闸**：`AssertWritable` 是唯一一道「不许写进只读库」的检查，
//     将来做「写回媒体目录」时，所有写操作都必须先过它（现在还没有写回，
//     所以它是给以后留的、已经能被单测钉住的口子）。
package overlay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// Store 是本包需要的存储能力（窄接口，便于注入测试替身）。
type Store interface {
	GetItem(ctx context.Context, id int64) (*store.Item, error)
	LibraryReadOnly(ctx context.Context, libraryID int64) (bool, error)
	ReadOnlyRoots(ctx context.Context) ([]string, error)
}

// Service 管理某个数据目录下的 overlay 层。
type Service struct {
	root string
	st   Store
}

// Stats 是某个库叠加层的占用情况（设置页显示用）。
type Stats struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// NewService 创建 overlay 服务。root 一般是 <数据目录>/overlay。
func NewService(root string, st Store) (*Service, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("overlay: 根目录不能为空")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建 overlay 根目录失败: %w", err)
	}
	return &Service{root: root, st: st}, nil
}

// Root 返回 overlay 根目录（日志与「文件在哪」的说明用）。
func (s *Service) Root() string { return s.root }

// LibraryDir 返回某个库的叠加层目录。
func (s *Service) LibraryDir(libraryID int64) string {
	return filepath.Join(s.root, fmt.Sprint(libraryID))
}

// ItemDir 返回某条条目在叠加层里的目录。
func (s *Service) ItemDir(libraryID, itemID int64) string {
	return filepath.Join(s.LibraryDir(libraryID), fmt.Sprint(itemID))
}

// target 解析「条目所属库 + 库是否只读」。ok=false 表示这个条目不需要 overlay
// （库不存在、或者库是普通的可写库 —— 那种库的图片仍走 images 包的缓存）。
func (s *Service) target(ctx context.Context, itemID int64) (libID int64, ok bool, err error) {
	it, err := s.st.GetItem(ctx, itemID)
	if err != nil {
		return 0, false, err
	}
	if it.LibraryID <= 0 {
		return 0, false, nil
	}
	ro, err := s.st.LibraryReadOnly(ctx, it.LibraryID)
	if err != nil {
		return 0, false, err
	}
	return it.LibraryID, ro, nil
}

// PutImage 把一张刮削到的图片复制进叠加层。srcPath 是已经下好的原图。
//
// 只读库才写（可写库走图片缓存，将来再谈写回媒体目录）；写入是
// 「临时文件 + rename」，不会留半截文件。
func (s *Service) PutImage(ctx context.Context, itemID int64, kind, srcPath string) (string, error) {
	libID, ok, err := s.target(ctx, itemID)
	if err != nil || !ok {
		return "", err
	}
	kind = safeName(kind)
	if kind == "" {
		return "", fmt.Errorf("overlay: 图片类型非法 %q", kind)
	}
	ext := strings.ToLower(filepath.Ext(srcPath))
	if ext == "" || len(ext) > 6 {
		ext = ".jpg"
	}
	dir := s.ItemDir(libID, itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建叠加层目录失败: %w", err)
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("打开源图片失败: %w", err)
	}
	defer func() { _ = src.Close() }()

	target := filepath.Join(dir, kind+ext)
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("写入叠加层图片失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("写入叠加层图片失败: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", fmt.Errorf("落盘叠加层图片失败: %w", err)
	}
	return target, nil
}

// PutImageIfMissing 同 PutImage，但已经在就什么都不做。
//
// 为什么单独一个入口：只读库的「刮削到的图片」可能是在打开只读**之前**
// 就缓存下来的 —— 那种图挂在缓存目录里，得在**每次取图时**顺手补一份到叠加层，
// 叠加层才会收敛到「这个库的全部刮削图片」。补的动作只做一次（有就跳过）。
func (s *Service) PutImageIfMissing(ctx context.Context, itemID int64, kind, srcPath string) (string, error) {
	libID, ok, err := s.target(ctx, itemID)
	if err != nil || !ok {
		return "", err
	}
	kind = safeName(kind)
	if kind == "" {
		return "", fmt.Errorf("overlay: 图片类型非法 %q", kind)
	}
	ext := strings.ToLower(filepath.Ext(srcPath))
	if ext == "" || len(ext) > 6 {
		ext = ".jpg"
	}
	target := filepath.Join(s.ItemDir(libID, itemID), kind+ext)
	if st, err := os.Stat(target); err == nil && st.Size() > 0 {
		return "", nil
	}
	return s.PutImage(ctx, itemID, kind, srcPath)
}

// WriteItemMeta 把一条条目的元数据快照写成 JSON（同样只对只读库生效）。
//
// 为什么要快照：只读库上的刮削成果只存在数据库里，用户想「带走/备份这一库的元数据」
// 时没有落点；写成 JSON 之后，叠加层就是这个库的「刮削成果包」。
func (s *Service) WriteItemMeta(ctx context.Context, itemID int64, v any) (string, error) {
	libID, ok, err := s.target(ctx, itemID)
	if err != nil || !ok {
		return "", err
	}
	dir := s.ItemDir(libID, itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建叠加层目录失败: %w", err)
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化元数据快照失败: %w", err)
	}
	target := filepath.Join(dir, "meta.json")
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(append(buf, '\n')); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("写入元数据快照失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("写入元数据快照失败: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", fmt.Errorf("落盘元数据快照失败: %w", err)
	}
	return target, nil
}

// Stats 统计某个库叠加层的文件数与占用（目录不存在就是 0）。
func (s *Service) Stats(libraryID int64) (Stats, error) {
	var out Stats
	root := s.LibraryDir(libraryID)
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // 文件刚被换掉/删掉：不算错，跳过这条
		}
		out.Files++
		out.Bytes += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return Stats{}, fmt.Errorf("统计叠加层失败: %w", err)
	}
	return out, nil
}

// AssertWritable 是「不许写进只读库」的唯一一道闸。
//
// 任何要落到媒体目录里的写操作，都必须先拿**绝对路径**问一遍它：
// 落在任一「只读」库的根路径之下就返回错误。现在还没有写回媒体目录的能力，
// 所以它拦不到东西 —— 这正是要留着的理由：等写回做出来时，
// 唯一的正确做法是「先过这道闸」，而不是在每个写点各写一遍判断。
func (s *Service) AssertWritable(ctx context.Context, target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("解析目标路径失败: %w", err)
	}
	roots, err := s.st.ReadOnlyRoots(ctx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if abs == rootAbs || strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
			return fmt.Errorf("媒体库目录是只读的（%s），不能写入 %s", root, target)
		}
	}
	return nil
}

// safeName 只放行 [a-z0-9_-]，避免 kind 里带出路径分隔符。
func safeName(kind string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(kind)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}
