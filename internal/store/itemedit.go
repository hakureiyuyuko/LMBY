package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// 本文件是「人工逐字段编辑 + 字段锁定」的落库逻辑。
//
// 与自动流程的分工：
//
//   - 自动流程（nfo 重读、TMDB 刮削、人工指定候选）走 ApplyItemMeta：
//     语义是「空值不覆盖」，并且**跳过 locked_fields 里的字段**。
//     所以人工改完并锁住的字段，重扫重刮都动不了它（DoD 里那条「手改字段不被覆盖」）。
//   - 人工编辑走 UpdateItemFields：语义相反，**写什么就是什么** ——
//     空串与 null 都会真的落库。用户是在显式表达「我就是要清空这一格」，
//     这跟自动流程「别把已有的好数据擦掉」的顾虑不是一回事。
//
// 字段名（title / providerIds / …）是 HTTP 接口、界面与 locked_fields 三处的共同契约，
// 只在下面这张表里定义一次；internal/scrape 的 Field* 常量由单测钉住与它一致
// （否则会出现「界面锁了 providerIds、刮削却在判断 providers」这种谁都没发现的错位）。

// ErrUnknownItemField 表示请求里出现了表里没有的字段名。
//
// 单独一个哨兵是因为要翻成 400：打错字段名（"providers"）如果被静默忽略，
// 用户会以为改生效了 —— 这类错最难发现，所以宁可报错。
var ErrUnknownItemField = errors.New("store: 不认识的条目字段")

// ErrInvalidItemValue 表示值本身不合法（类型不对 / 超出范围 / 日期格式错）。
//
// 同样翻成 400：这是用户填错了，不是服务器出错。
var ErrInvalidItemValue = errors.New("store: 条目字段值不合法")

// ItemFieldKind 是字段在**界面上**的值形态（不是数据库列类型）。
type ItemFieldKind string

const (
	ItemFieldText  ItemFieldKind = "text"  // 文本
	ItemFieldInt   ItemFieldKind = "int"   // 整数
	ItemFieldFloat ItemFieldKind = "float" // 小数（评分）
	ItemFieldList  ItemFieldKind = "list"  // 字符串数组（流派 / 制片公司）
	ItemFieldMap   ItemFieldKind = "map"   // 键值对（provider id）
	ItemFieldDate  ItemFieldKind = "date"  // 日期（YYYY-MM-DD）
)

// itemUnitMinutes 标记「界面按分钟填、库里存 tick」的字段（目前只有时长）。
//
// 单位单独写一笔而不是按字段名去 if：界面上写着「时长（分钟）」，
// 库里是 100ns 的 tick（与 ffprobe 探测出来的单位一致），
// 换算只在这一处发生。
const itemUnitMinutes = "minutes"

// ItemField 是一个可编辑（可锁定）的字段。
type ItemField struct {
	Name string        `json:"name"`
	Kind ItemFieldKind `json:"kind"`
	// Unit 是界面上的单位（空表示就用 Kind 本身描述）。
	Unit string `json:"unit,omitempty"`
	// Column 是数据库列名，只在内部用（界面上不该出现列名）。
	Column string `json:"-"`
}

// itemFields 是可编辑字段的全集，顺序即界面上的展示顺序。
//
// 这一组名字与 internal/scrape 的 Field* 常量、migrations 里的列一一对应；
// 新增字段时要同时改三处，`TestItemFieldsMatchScrapeFields` 与
// `TestApplyItemMetaGuardsEveryEditableField` 会拦住漏改。
var itemFields = []ItemField{
	{Name: "title", Column: "title", Kind: ItemFieldText},
	{Name: "originalTitle", Column: "original_title", Kind: ItemFieldText},
	{Name: "year", Column: "year", Kind: ItemFieldInt},
	{Name: "overview", Column: "overview", Kind: ItemFieldText},
	{Name: "tagline", Column: "tagline", Kind: ItemFieldText},
	{Name: "runtime", Column: "runtime_ticks", Kind: ItemFieldInt, Unit: itemUnitMinutes},
	{Name: "rating", Column: "community_rating", Kind: ItemFieldFloat},
	{Name: "officialRating", Column: "official_rating", Kind: ItemFieldText},
	{Name: "genres", Column: "genres", Kind: ItemFieldList},
	{Name: "studios", Column: "studios", Kind: ItemFieldList},
	{Name: "providerIds", Column: "provider_ids", Kind: ItemFieldMap},
	{Name: "premiereDate", Column: "premiere_date", Kind: ItemFieldDate},
}

// ItemFields 返回全部可编辑字段（副本，调用方改不动表）。
func ItemFields() []ItemField {
	out := make([]ItemField, len(itemFields))
	copy(out, itemFields)
	return out
}

// ItemFieldNames 返回全部字段名，顺序与 ItemFields 一致（用于错误提示）。
func ItemFieldNames() []string {
	out := make([]string, 0, len(itemFields))
	for _, f := range itemFields {
		out = append(out, f.Name)
	}
	return out
}

// LookupItemField 按字段名查表。
func LookupItemField(name string) (ItemField, bool) {
	for _, f := range itemFields {
		if f.Name == name {
			return f, true
		}
	}
	return ItemField{}, false
}

// FieldLocked 判断某个字段是否在锁定集合里。
//
// 自动流程与界面都用它判断「这个字段还能不能写」——
// 两边判断不一致会出现「界面显示已锁定、刮削照样覆盖」。
func FieldLocked(lockedFields []string, name string) bool {
	for _, f := range lockedFields {
		if f == name {
			return true
		}
	}
	return false
}

// SortTitle 由标题派生排序标题：去掉开头的冠词，让《The Matrix》排在 M 而不是 T。
//
// 它原本在 internal/scrape 里（自动刮削用它填 sort_title），挪过来是因为
// 人工编辑标题时也算一遍 —— 「改完标题、列表里的顺序还是旧的」是一眼能看出来的 bug。
func SortTitle(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, article) {
			t = t[len(article):]
			break
		}
	}
	return strings.TrimSpace(strings.TrimLeft(t, " ._-·、|"))
}

// UpdateItemFields 写入人工编辑的字段与锁定集合。
//
// values 的键是字段名，值是**界面形态**的值（见 ItemField.Kind / Unit）：
// 文本、整数的年份、分钟数的时长、评分、字符串数组、键值对、YYYY-MM-DD。
// 换算成列值（tick / jsonb / date）在本文件里做，调用方不需要知道列的样子。
//
// lockedFields 为 nil 表示不改锁定集合（空切片表示「全部解锁」）。
// matchState 非空时同时更新匹配状态（人工接手「待确认 / 没找到」的条目时用）。
func (s *Store) UpdateItemFields(ctx context.Context, itemID int64, values map[string]any,
	lockedFields *[]string, matchState string) error {
	sql, args, err := itemEditStatement(itemID, values, lockedFields, matchState)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		// 改标题/年份撞上「同一库里同名同年只允许一条」的唯一索引。
		// 这与刮削时的撞索引是同一件事（同一个作品被扫成了两条），交给上层翻成 409。
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: %w", ErrAlreadyExists, err)
		}
		return fmt.Errorf("更新条目失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// itemEditStatement 拼出人工编辑的 UPDATE 语句。
//
// 单独一个纯函数是为了能离线单测：字段名认不认、值类型对不对、
// 排序标题有没有跟着标题走 —— 这些都不该只能靠在真库上试。
func itemEditStatement(itemID int64, values map[string]any, lockedFields *[]string,
	matchState string) (string, []any, error) {
	if len(values) == 0 && lockedFields == nil && matchState == "" {
		return "", nil, errors.New("没有需要更新的字段")
	}

	// 不认识的字段名先报错：下面按表遍历，未知键会被静默跳过，
	// 而「静默跳过」正是打错字段名最糟糕的结局。
	for name := range values {
		if _, ok := LookupItemField(name); !ok {
			return "", nil, unknownFieldErr(name)
		}
	}

	var sets []string
	var args []any
	for _, f := range itemFields {
		v, ok := values[f.Name]
		if !ok {
			continue
		}
		dbv, err := itemFieldValue(f, v)
		if err != nil {
			return "", nil, err
		}
		args = append(args, dbv)
		cast := ""
		if f.Kind == ItemFieldList || f.Kind == ItemFieldMap {
			cast = "::jsonb"
		}
		sets = append(sets, fmt.Sprintf("%s = $%d%s", f.Column, len(args), cast))
	}

	// 排序标题跟着标题走：sort_title 不单独编辑（它是派生值），
	// 否则改完标题后列表里的排序位置还是旧的。
	if t, ok := values["title"].(string); ok && strings.TrimSpace(t) != "" {
		args = append(args, SortTitle(t))
		sets = append(sets, fmt.Sprintf("sort_title = $%d", len(args)))
	}

	if len(values) > 0 {
		// 人工写进去的值就是「人工来源」。注意不改 match_state：
		// 改一个字段不等于把整条标记成「人工处理」（那会让自动刮削整条跳过），
		// 哪些字段不能被覆盖由 locked_fields 逐字段表达。
		sets = append(sets, "metadata_source = 'manual'")
	}

	if lockedFields != nil {
		names := make([]string, 0, len(*lockedFields))
		for _, n := range *lockedFields {
			if _, ok := LookupItemField(n); !ok {
				return "", nil, unknownFieldErr(n)
			}
			names = append(names, n)
		}
		sort.Strings(names)
		args = append(args, jsonArray(names))
		sets = append(sets, fmt.Sprintf("locked_fields = $%d::jsonb", len(args)))
	}

	if matchState != "" {
		args = append(args, matchState)
		sets = append(sets, fmt.Sprintf("match_state = $%d", len(args)))
	}

	args = append(args, itemID)
	sql := "update media_items set " + strings.Join(sets, ", ") +
		", updated_at = now() where id = $" + fmt.Sprint(len(args)) + " and deleted_at is null"
	return sql, args, nil
}

// itemFieldValue 把界面形态的值校验并换算成可以直接写库的列值。
func itemFieldValue(f ItemField, v any) (any, error) {
	switch f.Kind {
	case ItemFieldText:
		s, ok := v.(string)
		if !ok {
			return nil, fieldTypeErr(f, "字符串")
		}
		return s, nil

	case ItemFieldInt:
		if v == nil {
			return nil, nil // 清空
		}
		n, ok := toInt64(v)
		if !ok {
			return nil, fieldTypeErr(f, "整数")
		}
		if f.Unit == itemUnitMinutes {
			if n < 0 {
				return nil, fmt.Errorf("%w: %s 不能是负数：%d", ErrInvalidItemValue, f.Name, n)
			}
			// 分钟 → tick（1 tick = 100ns），与 ffprobe 探测出来的 runtime_ticks 同单位
			return n * 60 * 10_000_000, nil
		}
		if n < -9999 || n > 99999 {
			return nil, fmt.Errorf("%w: %s 超出合理范围：%d", ErrInvalidItemValue, f.Name, n)
		}
		return int32(n), nil

	case ItemFieldFloat:
		switch x := v.(type) {
		case nil:
			return nil, nil // 清空
		case float64:
			if x < 0 || x > 10 {
				return nil, fmt.Errorf("%w: %s 应当是 0~10（与 TMDB 一致），收到 %v",
					ErrInvalidItemValue, f.Name, x)
			}
			return x, nil
		case int:
			return itemFieldValue(f, float64(x))
		}
		return nil, fieldTypeErr(f, "小数")

	case ItemFieldList:
		ss, ok := v.([]string)
		if !ok {
			return nil, fieldTypeErr(f, "字符串数组")
		}
		return jsonArray(ss), nil

	case ItemFieldMap:
		mm, ok := v.(map[string]string)
		if !ok {
			return nil, fieldTypeErr(f, "键值对象")
		}
		return jsonMap(mm), nil

	case ItemFieldDate:
		switch x := v.(type) {
		case nil:
			return nil, nil // 清空
		case time.Time:
			return x, nil
		case string:
			s := strings.TrimSpace(x)
			if s == "" {
				return nil, nil // 空串等同于清空
			}
			t, err := time.Parse("2006-01-02", s)
			if err != nil {
				return nil, fmt.Errorf("%w: %s 要写成 YYYY-MM-DD（收到 %q）",
					ErrInvalidItemValue, f.Name, x)
			}
			return t, nil
		}
		return nil, fieldTypeErr(f, "日期（YYYY-MM-DD）")
	}
	return nil, fmt.Errorf("字段 %s 的形态 %q 没有对应的落库方式", f.Name, f.Kind)
}

func fieldTypeErr(f ItemField, want string) error {
	return fmt.Errorf("%w: 字段 %s 需要%s", ErrInvalidItemValue, f.Name, want)
}

func unknownFieldErr(name string) error {
	return fmt.Errorf("%w: %q（可用：%s）", ErrUnknownItemField, name, strings.Join(ItemFieldNames(), "、"))
}

// toInt64 把常见的整数类型归一成 int64（JSON 解出来是 int64，测试里常写 int）。
func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	}
	return 0, false
}

// ScrapeTaskPriority 是刮削任务的优先级：比探测（0）高。
//
// 探测是纯背景工作（网盘上一条要十几秒），刮削是用户点了就想看结果的事 ——
// 同优先级时「刚扫完的 300 条待探测」会把刮削压到十几分钟之后（实盘踩到过）。
const ScrapeTaskPriority = 10

// EnqueueItemScrape 给单个条目排一次刮削（条目编辑界面上的「重新刮削」）。
//
// dedupe key 与批量入队相同（item:<id>），所以不会重复排队。
// force 时如果已经有一条**待处理**的同条目任务，直接把它升级成 force，
// 而不是再排一条 —— 否则用户点「重新刮削」会觉得没反应（任务其实早在队列里了）。
func (s *Store) EnqueueItemScrape(ctx context.Context, itemID int64, force bool) (int64, bool, error) {
	payload := map[string]any{"itemId": itemID}
	if force {
		payload["force"] = true
	}
	id, created, err := s.Enqueue(ctx, TaskKindScrape, payload, EnqueueOptions{
		Priority:  ScrapeTaskPriority,
		DedupeKey: fmt.Sprintf("item:%d", itemID),
	})
	if err != nil || created || !force {
		return id, created, err
	}

	// 已有待处理任务：把 force 补进去（只动 pending，正在跑的那条不打扰）。
	// 任务若已经在跑，这次 force 就生效不了 —— 调用方从 enqueued=false
	// 就知道「不是新排的」，界面上提示稍后刷新即可。
	if _, err := s.pool.Exec(ctx,
		`update tasks set payload = payload || '{"force": true}'::jsonb, run_at = now()
		 where id = $1 and state = 'pending'`, id); err != nil {
		return id, false, fmt.Errorf("把已排队的刮削任务升级为 force 失败: %w", err)
	}
	return id, false, nil
}
