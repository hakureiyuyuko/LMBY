package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// maxImageBytes 限制单张下载大小，防止一个奇怪的 URL 把磁盘写满。
const maxImageBytes = 16 << 20

// fetchOnce 回源取一张图。
//
// 同一张图并发只下一次：一个详情页会同时要海报、背景、logo、剧照，
// 不去重就会重复下载同一张。
func (s *Service) fetchOnce(ctx context.Context, item *store.Item, kind string) (*store.Image, error) {
	if s.client == nil {
		return nil, nil
	}
	unlock := s.lockKey(fmt.Sprintf("%d:%s", item.ID, kind))
	defer unlock()

	// 双检：等锁期间可能已经被别的请求下好了
	rows, err := s.st.ListImages(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	if img := bestOfKind(rows, kind); img != nil {
		return img, nil
	}

	path, size, err := s.providerImagePath(ctx, item, kind)
	if err != nil || path == "" {
		return nil, err
	}

	file, err := s.download(ctx, path, size)
	if err != nil {
		return nil, err
	}
	w, h, _ := probeSize(file)
	var sizeBytes int64
	if st, err := os.Stat(file); err == nil {
		sizeBytes = st.Size()
	}
	if err := s.st.UpsertImage(ctx, item.ID, kind, file, store.ImageSourceRemote, w, h, sizeBytes, 0); err != nil {
		return nil, err
	}
	s.log.Info("已回源取图", "itemId", item.ID, "kind", kind, "bytes", sizeBytes)

	rows, err = s.st.ListImages(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	return bestOfKind(rows, kind), nil
}

// providerImagePath 问 provider 要「这个条目这类图」的文件路径与尺寸代号。
//
// 只取详情接口里的主图（PosterPath / BackdropPath / StillPath）：
// 详情本来就进 provider_cache，不会为了图片多打接口；
// 候选列表（provider.Images）留给以后「换一张图」的界面用。
func (s *Service) providerImagePath(ctx context.Context, item *store.Item, kind string) (string, string, error) {
	switch item.Kind {
	case "movie":
		if kind != "poster" && kind != "fanart" && kind != "backdrop" {
			return "", "", nil
		}
		id := atoiOrZero(item.ProviderIDs["tmdb"])
		if id == 0 {
			return "", "", nil
		}
		m, err := s.client.Movie(ctx, id, "")
		if err != nil {
			return "", "", err
		}
		if kind == "poster" {
			return m.PosterPath, "w500", nil
		}
		return m.BackdropPath, "w1280", nil

	case "series":
		if kind != "poster" && kind != "fanart" && kind != "backdrop" {
			return "", "", nil
		}
		id := atoiOrZero(item.ProviderIDs["tmdb"])
		if id == 0 {
			return "", "", nil
		}
		det, err := s.client.Series(ctx, id, "")
		if err != nil {
			return "", "", err
		}
		if kind == "poster" {
			return det.PosterPath, "w500", nil
		}
		return det.BackdropPath, "w1280", nil

	case "season":
		if kind != "poster" {
			return "", "", nil
		}
		seriesID, err := s.seriesProviderID(ctx, item)
		if err != nil || seriesID == 0 {
			return "", "", err
		}
		det, err := s.client.Season(ctx, seriesID, int(numOr(item.SeasonNum, 0)), "")
		if err != nil {
			return "", "", err
		}
		return det.PosterPath, "w500", nil

	case "episode":
		if kind != "thumb" && kind != "poster" {
			return "", "", nil
		}
		seriesID, err := s.seriesProviderID(ctx, item)
		if err != nil || seriesID == 0 {
			return "", "", err
		}
		det, err := s.client.Episode(ctx, seriesID,
			int(numOr(item.SeasonNum, 1)), int(numOr(item.EpisodeNum, 0)), "")
		if err != nil {
			return "", "", err
		}
		return det.StillPath, "w300", nil
	}
	return "", "", nil
}

// seriesProviderID 取条目所属剧集在 provider 上的 id（季/集的图靠它定位）。
func (s *Service) seriesProviderID(ctx context.Context, item *store.Item) (int, error) {
	if item.SeriesID == nil || *item.SeriesID == 0 {
		return 0, nil
	}
	series, err := s.st.GetItem(ctx, *item.SeriesID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return atoiOrZero(series.ProviderIDs["tmdb"]), nil
}

// download 把 provider 的图下到 remote/ 目录，按 URL 内容寻址（同一张图不会存两份）。
//
// remote/ 里的文件算数据（库里有记录指着它），不参与缓存清理；
// 缩放的产物在 cache/ 里，那个才按上限清理。
func (s *Service) download(ctx context.Context, path, size string) (string, error) {
	rawURL := s.client.ImageURL(path, size)
	sum := sha256.Sum256([]byte(rawURL))
	key := hex.EncodeToString(sum[:])

	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" || len(ext) > 5 {
		ext = ".jpg"
	}
	dir := filepath.Join(s.pipe.dir, "remote", key[:2])
	target := filepath.Join(dir, key+ext)

	if st, err := os.Stat(target); err == nil && st.Size() > 0 {
		return target, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建图片目录失败: %w", err)
	}

	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("构造图片请求失败: %w", err)
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载图片失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载图片失败: HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(dir, "dl-*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("写入图片失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("写入图片失败: %w", err)
	}
	if n == 0 {
		return "", errors.New("下载到 0 字节的图片")
	}
	if _, _, _, err := probeImage(tmpName); err != nil {
		return "", fmt.Errorf("下载到的不是能识别的图片: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", fmt.Errorf("落盘图片失败: %w", err)
	}
	return target, nil
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
