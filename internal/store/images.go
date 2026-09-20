package store

import (
	"context"
	"fmt"
	"time"
)

// 图片来源（images.source）。
const (
	// ImageSourceLocal 是媒体目录里的本地图 —— 覆盖顺序里优先级最高。
	ImageSourceLocal = "local"
	// ImageSourceRemote 是从 provider 下载后放进 LMBY 缓存目录的图。
	ImageSourceRemote = "remote"
	// ImageSourceUploaded 是用户手选/上传的图（界面还没做，先把语义留出来）。
	ImageSourceUploaded = "uploaded"
)

// Image 是一张图片的登记信息。二进制永不入库，只存路径与元信息。
type Image struct {
	ID        int64     `json:"id"`
	ItemID    int64     `json:"itemId"`
	Kind      string    `json:"kind"`
	Path      string    `json:"path"`
	Source    string    `json:"source"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	SizeBytes int64     `json:"sizeBytes"`
	Lang      string    `json:"lang,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListImages 列出某个条目的全部图片，按 kind 排好序。
func (s *Store) ListImages(ctx context.Context, itemID int64) ([]Image, error) {
	rows, err := s.pool.Query(ctx,
		`select id, item_id, kind, path, source,
		        coalesce(width, 0), coalesce(height, 0),
		        coalesce(size_bytes, 0), lang, created_at
		 from images where item_id = $1
		 order by kind, id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("读取图片列表失败: %w", err)
	}
	defer rows.Close()

	var out []Image
	for rows.Next() {
		var img Image
		if err := rows.Scan(&img.ID, &img.ItemID, &img.Kind, &img.Path, &img.Source,
			&img.Width, &img.Height, &img.SizeBytes, &img.Lang, &img.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取图片行失败: %w", err)
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

// UpsertImage 登记一张图片（只存路径与元信息，二进制永不入库）。
//
// width/height 允许传 0：扫描阶段不去打开每个图片文件（网盘上 1200 张图
// 逐个 DecodeConfig 要多花十几秒），等真正要输出这张图时再补齐。
func (s *Store) UpsertImage(ctx context.Context, itemID int64, kind, path, source string, width, height int, size, mtimeNS int64) error {
	if source == "" {
		source = ImageSourceLocal
	}
	_, err := s.pool.Exec(ctx,
		`insert into images (item_id, kind, path, source, width, height, size_bytes, mtime_ns)
		 values ($1, $2, $3, $4, nullif($5, 0), nullif($6, 0), $7, $8)
		 on conflict (item_id, kind, path) do update
		   set source = excluded.source,
		       width = coalesce(excluded.width, images.width),
		       height = coalesce(excluded.height, images.height),
		       size_bytes = excluded.size_bytes,
		       mtime_ns = excluded.mtime_ns`,
		itemID, kind, path, source, width, height, size, mtimeNS)
	if err != nil {
		return fmt.Errorf("登记图片失败: %w", err)
	}
	return nil
}

// SetImageDimensions 补上图片尺寸（首次输出这张图时顺手记下来）。
func (s *Store) SetImageDimensions(ctx context.Context, imageID int64, width, height int) error {
	if width <= 0 || height <= 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`update images set width = $2, height = $3 where id = $1`, imageID, width, height)
	if err != nil {
		return fmt.Errorf("写入图片尺寸失败: %w", err)
	}
	return nil
}

// DeleteImagesExcept 删除某个条目下不在给定路径集合中、且来自本地文件的图片记录。
//
// 只删 source='local'：远程下载图与用户上传图不能因为「本轮扫描没看到」而被清掉。
func (s *Store) DeleteImagesExcept(ctx context.Context, itemID int64, keepPaths []string) (int64, error) {
	if keepPaths == nil {
		keepPaths = []string{}
	}
	tag, err := s.pool.Exec(ctx,
		`delete from images
		 where item_id = $1 and source = 'local' and not (path = any($2))`, itemID, keepPaths)
	if err != nil {
		return 0, fmt.Errorf("清理图片记录失败: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountImages 统计图片数（库列表展示用）。
func (s *Store) CountImages(ctx context.Context, libraryID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`select count(*) from images g
		 join media_items i on i.id = g.item_id
		 where i.library_id = $1`, libraryID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计图片失败: %w", err)
	}
	return n, nil
}
