package logbuf

// 环形缓冲 + 过滤的规则用表测试钉住。
//
// 为什么单独测：这块的错法都很「安静」—— 环覆盖时 dropped 不涨（界面以为日志只有这些）、
// WithAttrs 之后日志不进环（换个子 logger 就丢）、级别/关键字过滤反了。
// 这些都是「看日志时才发现不对」的东西，值得用单测挡住。

import (
	"io"
	"log/slog"
	"testing"
)

func newTestBuffer(capacity int) (*slog.Logger, *Buffer) {
	h := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	b := New(h, capacity)
	return slog.New(b), b
}

func TestRingKeepsNewestAndCountsDropped(t *testing.T) {
	log, buf := newTestBuffer(3)
	for i := 1; i <= 5; i++ {
		log.Info("第条", "n", i)
	}
	entries, st := buf.Snapshot(10, ParseLevel(""), "")
	if st.Capacity != 3 || st.Count != 3 {
		t.Fatalf("容量/条数不对：%+v", st)
	}
	if st.Dropped != 2 {
		t.Fatalf("被覆盖的条数应为 2（写了 5 条、只留 3 条），实际 %d", st.Dropped)
	}
	// 倒序：最新在前
	if len(entries) != 3 || entries[0].Msg != "第条" || entries[0].Attrs["n"] != int64(5) {
		t.Fatalf("应保留最后 3 条且最新的在前，实际 %+v", entries)
	}
	if entries[2].Attrs["n"] != int64(3) {
		t.Fatalf("最旧的一条应是 n=3，实际 %+v", entries[2])
	}
}

func TestLevelFilter(t *testing.T) {
	log, buf := newTestBuffer(10)
	log.Debug("调试")
	log.Info("信息")
	log.Warn("警告")
	log.Error("错误")

	// 空串 = 不过滤
	all, _ := buf.Snapshot(10, ParseLevel(""), "")
	if len(all) != 4 {
		t.Fatalf("不过滤时应 4 条，实际 %d", len(all))
	}
	// warn 及以上
	warns, _ := buf.Snapshot(10, ParseLevel("warn"), "")
	if len(warns) != 2 {
		t.Fatalf("warn 以上应 2 条（WARN/ERROR），实际 %d：%+v", len(warns), warns)
	}
	// 认不出来的级别当作「不过滤」（而不是什么都不返回）
	unknown, _ := buf.Snapshot(10, ParseLevel("nonsense"), "")
	if len(unknown) != 4 {
		t.Fatalf("认不出的级别应不过滤，实际 %d", len(unknown))
	}
}

func TestQueryMatchesMessageAndAttrs(t *testing.T) {
	log, buf := newTestBuffer(10)
	log.Info("播放失败", "channel", 29, "err", "连接超时")
	log.Info("扫描完成", "library", 1)

	byMsg, _ := buf.Snapshot(10, ParseLevel(""), "播放")
	if len(byMsg) != 1 || byMsg[0].Msg != "播放失败" {
		t.Fatalf("按消息搜应命中 1 条，实际 %+v", byMsg)
	}
	byAttr, _ := buf.Snapshot(10, ParseLevel(""), "超时")
	if len(byAttr) != 1 {
		t.Fatalf("按字段值搜应命中 1 条，实际 %+v", byAttr)
	}
	byAttrKey, _ := buf.Snapshot(10, ParseLevel(""), "LIBRARY")
	if len(byAttrKey) != 1 {
		t.Fatalf("按字段名搜应命中 1 条（且忽略大小写），实际 %+v", byAttrKey)
	}
	none, _ := buf.Snapshot(10, ParseLevel(""), "不存在的词")
	if len(none) != 0 {
		t.Fatalf("搜不到应返回空，实际 %+v", none)
	}
}

func TestWithAttrsSharesRing(t *testing.T) {
	log, buf := newTestBuffer(10)
	// 换个子 logger 之后，日志必须**照样进环**（WithAttrs 会返回新 handler）
	sub := log.With("module", "livetv")
	sub.Info("起播失败", "channel", 3)

	entries, st := buf.Snapshot(10, ParseLevel(""), "")
	if st.Count != 1 {
		t.Fatalf("子 logger 的日志也要进环，实际 %+v", st)
	}
	if entries[0].Attrs["module"] != "livetv" || entries[0].Attrs["channel"] != int64(3) {
		t.Fatalf("字段应保留（含 With 带的），实际 %+v", entries[0].Attrs)
	}
}

func TestLimitCapsReturnedEntries(t *testing.T) {
	log, buf := newTestBuffer(100)
	for i := 0; i < 50; i++ {
		log.Info("x")
	}
	entries, st := buf.Snapshot(10, ParseLevel(""), "")
	if len(entries) != 10 {
		t.Fatalf("limit=10 应只返回 10 条，实际 %d", len(entries))
	}
	if st.Count != 50 {
		t.Fatalf("total 应是环里的总条数 50，实际 %d", st.Count)
	}
}

func TestNilBufferIsSafe(t *testing.T) {
	var b *Buffer
	entries, st := b.Snapshot(10, ParseLevel(""), "")
	if len(entries) != 0 || st.Count != 0 {
		t.Fatalf("未接入缓冲时应安全返回空，实际 %+v %+v", entries, st)
	}
}
