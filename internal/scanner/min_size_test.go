package scanner

import "testing"

// 阈值是「挡垃圾」还是「挡正常片源」，差别只有几十 KB —— 用一条测试把它钉住。
//
// 真实踩坑（2026-09-22）：用户的「连载动画」库里 63 个文件被当垃圾跳过，
// 因为它们是 **28 秒的 1080p h264 短片**，单文件只有 646 KB（ffprobe 确认有效），
// 而当时的默认阈值是 1 MiB。
func TestDefaultMinFileSizeKeepsShortClips(t *testing.T) {
	const shortestRealClip = 646 * 1024 // 实测：28 秒 1080p h264 ≈ 646 KB
	if defaultMinFileSize > shortestRealClip {
		t.Fatalf("默认阈值 %d 字节会把 %d 字节的正常短片当垃圾（阈值只能用来挡缩略图/残片）",
			defaultMinFileSize, shortestRealClip)
	}
	// 也别小到形同虚设：一张缩略图/被改名的文本不该被当成视频
	if defaultMinFileSize < 16*1024 {
		t.Fatalf("默认阈值 %d 字节太小，挡不住缩略图/残片", defaultMinFileSize)
	}
}
