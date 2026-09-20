package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// 本文件是「条目详情 / 人工编辑（含字段锁定）」的接口。
//
// 为什么单独一页而不是塞进人工匹配：人工匹配解决「机器猜错了是哪一部」，
// 条目编辑解决「数据本身不对/不够」（标题、简介、年份、流派、provider id…）。
// 两件事的入口都在界面上，但动作不一样 —— 前者的结果是状态标 manual，
// 后者写的是具体字段，并且**锁住**它们，让重扫重刮都不能再覆盖。

// itemFieldResponse 是界面上一个可编辑字段的描述。
type itemFieldResponse struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Unit   string `json:"unit,omitempty"`
	Locked bool   `json:"locked"`
}

// itemDetail 是条目详情的响应体（编辑界面的数据源）。
func (s *Server) itemDetail(item *store.Item) map[string]any {
	fields := store.ItemFields()
	out := make([]itemFieldResponse, 0, len(fields))
	for _, f := range fields {
		out = append(out, itemFieldResponse{
			Name:   f.Name,
			Kind:   string(f.Kind),
			Unit:   f.Unit,
			Locked: store.FieldLocked(item.LockedFields, f.Name),
		})
	}
	return map[string]any{
		"item":   item,
		"fields": out,
		// 没配元数据源时界面就不该显示「重新刮削」（点了也只会报错）
		"scrapeConfigured": s.scrapeConfigured(),
	}
}

// handleGetItem 返回条目详情（含可编辑字段与锁定状态）。
func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.itemDetail(item))
}

// itemEditRequest 是人工编辑的请求体。
type itemEditRequest struct {
	// Fields 里**出现过的键**才会被写入；值为 null 表示清空。
	//
	// 与自动刮削的「空值不覆盖」相反：人工编辑是显式表达，
	// 留空就是「我要这一格是空的」。
	Fields map[string]json.RawMessage `json:"fields"`
	// LockedFields 非 nil 时整体替换锁定集合（空数组 = 全部解锁）。
	LockedFields *[]string `json:"lockedFields"`
}

// handleUpdateItem 人工编辑条目字段并（可选）更新锁定集合。
func (s *Server) handleUpdateItem(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}

	var req itemEditRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	values, err := decodeItemFields(req.Fields)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(values) == 0 && req.LockedFields == nil {
		writeError(w, http.StatusBadRequest, "没有需要更新的字段（fields 与 lockedFields 至少要给一个）")
		return
	}
	if title, ok := values["title"].(string); ok && strings.TrimSpace(title) == "" {
		writeError(w, http.StatusBadRequest, "标题不能为空 —— 条目在列表里会变成「无标题」，没法辨认")
		return
	}

	// 「人工编辑」正是「待确认 / 没找到」这两类状态在等的处理，
	// 编辑之后不该再挂在待办列表上，所以顺手标成 manual。
	// 其它状态（local / matched / nfo）只改字段、状态不动：
	// 改一格不等于把整条交给人工 —— 哪些字段不能被覆盖由 locked_fields 逐字段表达，
	// 没锁的字段以后还是能被刮削补上。
	matchState := ""
	if item.MatchState == store.MatchStateReview || item.MatchState == store.MatchStateFailed {
		matchState = store.MatchStateManual
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if err := s.store.UpdateItemFields(ctx, item.ID, values, req.LockedFields, matchState); err != nil {
		s.itemEditError(w, err)
		return
	}

	// 重新读一遍：锁定集合与状态都可能刚变，直接把最新的详情回给界面。
	updated, err := s.store.GetItem(ctx, item.ID)
	if err != nil {
		s.notFoundOrError(w, err)
		return
	}

	s.log.Info("人工编辑条目",
		"itemId", item.ID, "fields", sortedKeys(values), "locked", updated.LockedFields,
		"matchState", updated.MatchState, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, s.itemDetail(updated))
}

// itemScrapeRequest 是「只重刮这一条」的请求体。
type itemScrapeRequest struct {
	// Force 为真时连已匹配（含 nfo / manual）的条目也重刮。
	Force bool `json:"force"`
}

// handleEnqueueItemScrape 给单个条目排一次刮削。
//
// 什么时候用：把锁住的字段解锁之后，想把 provider 的值重新拉回来。
// （批量入口在媒体库页；这里是为了「改完这一条马上重刮一次」的手感，
// 不用为了一个条目去整个库里 force 重刮。）
func (s *Server) handleEnqueueItemScrape(w http.ResponseWriter, r *http.Request) {
	item, ok := s.itemForRequest(w, r)
	if !ok {
		return
	}
	if !s.scrapeConfigured() {
		writeError(w, http.StatusConflict, "未配置元数据源（config.toml 的 [tmdb] 段或 LMBY_TMDB_READ_TOKEN）")
		return
	}

	var req itemScrapeRequest
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	taskID, created, err := s.store.EnqueueItemScrape(ctx, item.ID, req.Force)
	if err != nil {
		s.serverError(w, "入队刮削任务失败", err)
		return
	}
	s.log.Info("已入队单条刮削",
		"itemId", item.ID, "force", req.Force, "taskId", taskID, "created", created, "username", usernameOf(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"taskId": taskID,
		// created=false 表示队列里已经有一条同条目任务（这次没新排）
		"enqueued": created,
		"force":    req.Force,
	})
}

// itemEditError 把写入错误翻成合适的 HTTP 状态。
func (s *Server) itemEditError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrUnknownItemField), errors.Is(err, store.ErrInvalidItemValue):
		// 用户填错了：字段名、类型、范围、日期格式
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "条目不存在")
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict,
			"同一库里已经有同名同年的条目 —— 改标题/年份撞了唯一索引。"+
				"如果是同一个作品被扫成了两条，请先处理重复的那条")
	default:
		s.serverError(w, "更新条目失败", err)
	}
}

// decodeItemFields 把请求里的字段值按字段形态解成 Go 值。
//
// 字段名必须在 store 的表里（打错字直接 400，不静默忽略）；
// 值的形态也要对得上（store 那边还会再校验一次范围）。
func decodeItemFields(raw map[string]json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(raw))
	for name, body := range raw {
		f, ok := store.LookupItemField(name)
		if !ok {
			return nil, fmt.Errorf("%w: %q（可用：%s）",
				store.ErrUnknownItemField, name, strings.Join(store.ItemFieldNames(), "、"))
		}
		v, err := decodeItemFieldValue(f, body)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

// decodeItemFieldValue 按字段形态解析一个值。null 表示清空。
func decodeItemFieldValue(f store.ItemField, body json.RawMessage) (any, error) {
	if isJSONNull(body) {
		// 清空：文本落空串，可空列落 NULL，数组/对象落空结构
		switch f.Kind {
		case store.ItemFieldText:
			return "", nil
		case store.ItemFieldList:
			return []string{}, nil
		case store.ItemFieldMap:
			return map[string]string{}, nil
		default:
			return nil, nil
		}
	}

	switch f.Kind {
	case store.ItemFieldText:
		var v string
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fieldDecodeErr(f, "字符串")
		}
		return v, nil
	case store.ItemFieldInt:
		var v int64
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fieldDecodeErr(f, "整数")
		}
		return v, nil
	case store.ItemFieldFloat:
		var v float64
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fieldDecodeErr(f, "数字")
		}
		return v, nil
	case store.ItemFieldList:
		var v []string
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fieldDecodeErr(f, "字符串数组")
		}
		return v, nil
	case store.ItemFieldMap:
		var v map[string]string
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fieldDecodeErr(f, "键值对象")
		}
		return v, nil
	case store.ItemFieldDate:
		var v string
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fieldDecodeErr(f, "日期字符串（YYYY-MM-DD）")
		}
		return v, nil
	}
	return nil, fmt.Errorf("字段 %s 的形态 %q 没有对应的解析方式", f.Name, f.Kind)
}

func isJSONNull(body json.RawMessage) bool {
	return strings.TrimSpace(string(body)) == "null"
}

func fieldDecodeErr(f store.ItemField, want string) error {
	return fmt.Errorf("字段 %s 需要%s", f.Name, want)
}

// sortedKeys 把字段名排一下序，只为日志好读。
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
