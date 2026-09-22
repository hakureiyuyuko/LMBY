package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 本文件是**收藏**（迁移 0012）。
//
// 三件事各占一个函数，职责不重叠：
//   - SetFavorite 写（幂等：同一个条目收藏两次不会报错，也不会多一行）；
//   - FavoriteState 读「单条条目的状态」——详情页要知道「我收藏了没」与「一共多少人收藏」；
//   - ListFavorites 读「我的收藏」列表。
//
// 口径：收藏**按账号**（M7 的地基），而「收藏数」是**全站**视角 —— 两个数字都从同一张表来，
// 不会漂移（多写一个计数器列就一定会漂移）。

// SetFavorite 收藏 / 取消收藏一个条目（幂等）。
func (s *Store) SetFavorite(ctx context.Context, userID, itemID int64, favorite bool) error {
	if userID <= 0 || itemID <= 0 {
		return errors.New("用户或条目非法")
	}
	if favorite {
		if _, err := s.pool.Exec(ctx,
			`insert into favorites (user_id, item_id) values ($1, $2)
			 on conflict (user_id, item_id) do nothing`, userID, itemID); err != nil {
			return fmt.Errorf("收藏失败: %w", err)
		}
		return nil
	}
	if _, err := s.pool.Exec(ctx,
		`delete from favorites where user_id = $1 and item_id = $2`, userID, itemID); err != nil {
		return fmt.Errorf("取消收藏失败: %w", err)
	}
	return nil
}

// FavoriteState 返回「这个账号收藏了没」与**全站收藏数**（详情页两个都要显示）。
//
// 一次查询取两个数（子查询），省一次往返；条目不存在时两个值分别是 false / 0。
func (s *Store) FavoriteState(ctx context.Context, userID, itemID int64) (bool, int, error) {
	var mine bool
	var total int
	err := s.pool.QueryRow(ctx,
		`select exists (select 1 from favorites where user_id = $1 and item_id = $2),
		        (select count(*)::int from favorites where item_id = $2)`,
		userID, itemID).Scan(&mine, &total)
	if err != nil {
		return false, 0, fmt.Errorf("读取收藏状态失败: %w", err)
	}
	return mine, total, nil
}

// ListFavorites 列出某个账号的收藏（按收藏时间倒序）。
//
// kind 为空表示不限类型；`deleted_at is null` 把已经扫不见的条目挡在外面
// （文件被删掉之后收藏行还在，但用户不该看到一个点不开的条目）。
// libs 是可见库（nil = 不过滤）：收藏里的条目也可能落在看不见的库里。
func (s *Store) ListFavorites(ctx context.Context, userID int64, kind string, limit, offset int, libs []int64) ([]Item, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	kind = strings.TrimSpace(kind)

	// 可见库条件与列表**共用**（count 也用它，否则「共 N 条」会和列出来的对不上）
	cond := `f.user_id = $1
		and media_items.deleted_at is null
		and ($2::text = '' or media_items.kind = $2)
		and ` + libraryFilter("media_items.library_id", "$5")

	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+`
		 from media_items
		 join favorites f on f.item_id = media_items.id
		 where `+cond+`
		 order by f.created_at desc, media_items.id desc
		 limit $3 offset $4`, userID, kind, limit, offset, libsArg(libs))
	if err != nil {
		return nil, 0, fmt.Errorf("查询收藏失败: %w", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return nil, 0, err
	}

	// count 单独拼一份条件：占位符必须**从 $1 连续排到最后一个引用**。
	// 直接复用 cond 再把 limit/offset 一起塞进来，会让 $3/$4 出现在参数表里
	// 却不在 SQL 里（PostgreSQL 直接报 42P18「could not determine data type of
	// parameter $3」—— 首页 500 就是这么来的）。
	countCond := `f.user_id = $1
		and media_items.deleted_at is null
		and ($2::text = '' or media_items.kind = $2)
		and ` + libraryFilter("media_items.library_id", "$3")

	var total int64
	if err := s.pool.QueryRow(ctx,
		`select count(*) from media_items
		 join favorites f on f.item_id = media_items.id
		 where `+countCond, userID, kind, libsArg(libs)).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计收藏失败: %w", err)
	}
	return items, total, nil
}

// ------------------------------------------------------------------ 播放列表 / 合集
//
// 一张表两种 kind（见 migrations/0013_playlists.sql）：
//   - `playlist`  私人：只有主人看得到、只有主人能改；
//   - `collection` 合集：所有人可见，任何管理员都能改。
//
// **权限判定只写在这一个文件里**（Viewer 的三个方法），调用方一律通过
// `GetPlaylistFor` / `UpdatePlaylist` 这类入口进 —— 每个接口各自判一次权限，
// 迟早会漏一处，而漏一处就是越权。

// Viewer 是「谁在看 / 谁在改」—— **权限判定的唯一载体**。
//
// 字段分两类：
//   - 列表归属：UserID / IsAdmin（这个文件里的 canSee/canEdit 用）；
//   - 库可见性与能力：restrict 之后只允许看白名单里的库，以及能不能转码 / 看直播
//     （见 permissions.go 的 ViewerFor 与 libraryFilter，读路径统一在 SQL 层过滤）。
type Viewer struct {
	UserID int64
	// IsAdmin 决定能不能碰别人的合集、以及能不能看到全部库。
	IsAdmin bool

	// libs：nil = 不限制（管理员 / 未开启按库限制）；空切片 = 什么都看不到。
	libs []int64

	// AllowTranscode / AllowLiveTV / MaxStreams：这个人能不能转码、能不能看直播、
	// 同时能开几条播放会话（0 = 用全局上限）。
	AllowTranscode bool
	AllowLiveTV    bool
	MaxStreams     int
}

// canSee 判断可见性：自己的列表，或者合集。
func (v Viewer) canSee(p Playlist) bool {
	return p.UserID == v.UserID || p.Kind == PlaylistKindCollection
}

// canEdit 判断可改性：自己的列表，或者是合集且自己是管理员。
func (v Viewer) canEdit(p Playlist) bool {
	if p.UserID == v.UserID {
		return true
	}
	return p.Kind == PlaylistKindCollection && v.IsAdmin
}

const (
	// PlaylistKindPrivate 是私人播放列表。
	PlaylistKindPrivate = "playlist"
	// PlaylistKindCollection 是对所有人可见的合集。
	PlaylistKindCollection = "collection"
)

var (
	// ErrPlaylistNotFound 既表示「真不存在」也表示「不给你看」—— 不区分，
	// 免得用「存在与否」的差异探出别人有哪些私人列表。
	ErrPlaylistNotFound = errors.New("列表不存在")
	// ErrPlaylistForbidden 表示「看得见但不给你改」（比如别人的合集、你不是管理员）。
	ErrPlaylistForbidden = errors.New("没有权限修改这个列表")
	// ErrPlaylistNameTaken 是重名（同名列表每人一份，不分大小写）。
	ErrPlaylistNameTaken = errors.New("已经有同名的列表了")
)

// InvalidInputError 是「调用方给的参数不合法」—— 与「服务端故障」分开，
// 因为 API 层要按它回 400 而不是 500（用户写错名字不该记成服务端故障，
// 否则日志里全是假警报，真正要看的 5xx 反而被淹没）。
type InvalidInputError struct{ Msg string }

func (e *InvalidInputError) Error() string { return e.Msg }

// invalidInput 造一个「参数不合法」错误。
func invalidInput(msg string) error { return &InvalidInputError{Msg: msg} }

// Playlist 是一个播放列表或合集（带界面要用的统计）。
type Playlist struct {
	ID       int64  `json:"id"`
	UserID   int64  `json:"userId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Overview string `json:"overview,omitempty"`
	// ItemCount 是列表里的条目数（**不含**已被删除的条目，与列表页看到的一致）。
	ItemCount int `json:"itemCount"`
	// CoverItemID 是列表里第一条**还在**且有顺序最靠前的那条（界面拿它当封面）。
	CoverItemID *int64    `json:"coverItemId,omitempty"`
	OwnerName   string    `json:"ownerName,omitempty"`
	Mine        bool      `json:"mine"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// playlistColumns 是读 Playlist 的固定列（两条查询共用，顺序必须一致）。
const playlistColumns = `p.id, p.user_id, p.name, p.kind, p.overview, p.created_at, p.updated_at,
	coalesce(u.username, ''), coalesce(u.display_name, ''),
	(select count(*)::int from playlist_items pi
	   join media_items mi on mi.id = pi.item_id and mi.deleted_at is null
	  where pi.playlist_id = p.id),
	(select pi.item_id from playlist_items pi
	   join media_items mi on mi.id = pi.item_id and mi.deleted_at is null
	  where pi.playlist_id = p.id
	  order by pi.sort_order, pi.added_at, pi.item_id limit 1)`

func scanPlaylist(row pgx.Row, v Viewer) (Playlist, error) {
	var p Playlist
	var ownerName, ownerDisplay string
	err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.Kind, &p.Overview, &p.CreatedAt, &p.UpdatedAt,
		&ownerName, &ownerDisplay, &p.ItemCount, &p.CoverItemID)
	if err != nil {
		return Playlist{}, err
	}
	// 界面上「谁建的合集」优先显示显示名
	p.OwnerName = ownerDisplay
	if p.OwnerName == "" {
		p.OwnerName = ownerName
	}
	p.Mine = p.UserID == v.UserID
	return p, nil
}

// ListPlaylists 列出「我的列表 + 所有合集」（我的在前，各自按最近改动倒序）。
func (s *Store) ListPlaylists(ctx context.Context, v Viewer) ([]Playlist, error) {
	rows, err := s.pool.Query(ctx,
		`select `+playlistColumns+`
		 from playlists p
		 left join users u on u.id = p.user_id
		 where p.user_id = $1 or p.kind = $2
		 order by (p.user_id = $1) desc, p.updated_at desc, p.id`, v.UserID, PlaylistKindCollection)
	if err != nil {
		return nil, fmt.Errorf("查询列表失败: %w", err)
	}
	defer rows.Close()

	out := []Playlist{}
	for rows.Next() {
		p, err := scanPlaylist(rows, v)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPlaylistFor 读一个列表（不可见就 ErrPlaylistNotFound）。
//
// 可见性在这里判两遍是有意的：SQL 先滤掉不可见的（列表查询必须这么做），
// Go 侧再用 canSee 把规则写死一次 —— 单条读的入口只有这一个，规则也只有一个实现，
// 将来改可见性（比如加入共享列表）改 canSee 就不会漏。
func (s *Store) GetPlaylistFor(ctx context.Context, v Viewer, id int64) (Playlist, error) {
	row := s.pool.QueryRow(ctx,
		`select `+playlistColumns+`
		 from playlists p
		 left join users u on u.id = p.user_id
		 where p.id = $1 and (p.user_id = $2 or p.kind = $3)`, id, v.UserID, PlaylistKindCollection)
	p, err := scanPlaylist(row, v)
	if errors.Is(err, pgx.ErrNoRows) {
		return Playlist{}, ErrPlaylistNotFound
	}
	if err != nil {
		return Playlist{}, fmt.Errorf("读取列表失败: %w", err)
	}
	if !v.canSee(p) {
		// 理论上到不了（SQL 已滤过）；真到了说明两处规则跑偏了，宁可当「不存在」
		return Playlist{}, ErrPlaylistNotFound
	}
	return p, nil
}

// CreatePlaylist 新建播放列表（kind 为空时按私人列表处理）。
//
// 建**合集**要求管理员：合集对所有人可见，不该人人可建。
func (s *Store) CreatePlaylist(ctx context.Context, v Viewer, kind, name, overview string) (Playlist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Playlist{}, invalidInput("列表名不能为空")
	}
	if len([]rune(name)) > 100 {
		return Playlist{}, invalidInput("列表名太长了（上限 100 字）")
	}
	if kind == "" {
		kind = PlaylistKindPrivate
	}
	if kind != PlaylistKindPrivate && kind != PlaylistKindCollection {
		return Playlist{}, invalidInput("kind 只能是 playlist 或 collection")
	}
	if kind == PlaylistKindCollection && !v.IsAdmin {
		return Playlist{}, ErrPlaylistForbidden
	}

	var id int64
	err := s.pool.QueryRow(ctx,
		`insert into playlists (user_id, name, kind, overview) values ($1, $2, $3, $4)
		 returning id`, v.UserID, name, kind, strings.TrimSpace(overview)).Scan(&id)
	if isUniqueViolation(err) {
		return Playlist{}, ErrPlaylistNameTaken
	}
	if err != nil {
		return Playlist{}, fmt.Errorf("新建列表失败: %w", err)
	}
	return s.GetPlaylistFor(ctx, v, id)
}

// UpdatePlaylist 改名 / 改说明（**nil 表示不动这一项**，空字符串表示清空）。
//
// 用指针而不是「空串=不改」：说明是可以被清空的，而「不改」与「清空」必须能区分开 ——
// 否则界面只改个名字就会把说明抹了。
func (s *Store) UpdatePlaylist(ctx context.Context, v Viewer, id int64, name, overview *string) (Playlist, error) {
	cur, err := s.GetPlaylistFor(ctx, v, id)
	if err != nil {
		return Playlist{}, err
	}
	if !v.canEdit(cur) {
		return Playlist{}, ErrPlaylistForbidden
	}
	newName := cur.Name
	if name != nil {
		newName = strings.TrimSpace(*name)
		if newName == "" {
			return Playlist{}, invalidInput("列表名不能为空")
		}
		if len([]rune(newName)) > 100 {
			return Playlist{}, invalidInput("列表名太长了（上限 100 字）")
		}
	}
	newOverview := cur.Overview
	if overview != nil {
		newOverview = strings.TrimSpace(*overview)
	}
	if _, err := s.pool.Exec(ctx,
		`update playlists set name = $2, overview = $3, updated_at = now() where id = $1`,
		id, newName, newOverview); err != nil {
		if isUniqueViolation(err) {
			return Playlist{}, ErrPlaylistNameTaken
		}
		return Playlist{}, fmt.Errorf("更新列表失败: %w", err)
	}
	return s.GetPlaylistFor(ctx, v, id)
}

// DeletePlaylist 删除列表（条目关系随级联一起走，不动媒体文件）。
func (s *Store) DeletePlaylist(ctx context.Context, v Viewer, id int64) error {
	cur, err := s.GetPlaylistFor(ctx, v, id)
	if err != nil {
		return err
	}
	if !v.canEdit(cur) {
		return ErrPlaylistForbidden
	}
	if _, err := s.pool.Exec(ctx, `delete from playlists where id = $1`, id); err != nil {
		return fmt.Errorf("删除列表失败: %w", err)
	}
	return nil
}

// AddPlaylistItems 往列表末尾追加条目，返回真的加进去几条。
//
// 三个细节：
//   - 用 `array_position` 保住调用方给的顺序（否则 Postgres 的返回顺序不可指望）；
//   - `deleted_at is null` 顺手把扫不见的条目挡在外面（不然列表里会多一个点不开的条目）；
//   - `on conflict do nothing` 让「重复加入」幂等（同一个条目加两次只留一条）。
func (s *Store) AddPlaylistItems(ctx context.Context, v Viewer, id int64, itemIDs []int64) (int, error) {
	cur, err := s.GetPlaylistFor(ctx, v, id)
	if err != nil {
		return 0, err
	}
	if !v.canEdit(cur) {
		return 0, ErrPlaylistForbidden
	}
	if len(itemIDs) == 0 {
		return 0, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var maxOrder int
	if err := tx.QueryRow(ctx,
		`select coalesce(max(sort_order), 0) from playlist_items where playlist_id = $1`, id).
		Scan(&maxOrder); err != nil {
		return 0, fmt.Errorf("读取列表末尾失败: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`insert into playlist_items (playlist_id, item_id, sort_order)
		 select $1, mi.id, $3 + row_number() over (order by array_position($2::bigint[], mi.id))
		 from media_items mi
		 where mi.id = any($2::bigint[]) and mi.deleted_at is null
		 on conflict (playlist_id, item_id) do nothing`, id, itemIDs, maxOrder)
	if err != nil {
		return 0, fmt.Errorf("加入列表失败: %w", err)
	}
	if _, err := tx.Exec(ctx, `update playlists set updated_at = now() where id = $1`, id); err != nil {
		return 0, fmt.Errorf("更新列表时间失败: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("提交事务失败: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RemovePlaylistItems 从列表里移除条目（幂等：不在列表里也算成功）。
func (s *Store) RemovePlaylistItems(ctx context.Context, v Viewer, id int64, itemIDs []int64) error {
	cur, err := s.GetPlaylistFor(ctx, v, id)
	if err != nil {
		return err
	}
	if !v.canEdit(cur) {
		return ErrPlaylistForbidden
	}
	if len(itemIDs) == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx,
		`delete from playlist_items where playlist_id = $1 and item_id = any($2::bigint[])`,
		id, itemIDs); err != nil {
		return fmt.Errorf("移出列表失败: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `update playlists set updated_at = now() where id = $1`, id); err != nil {
		return fmt.Errorf("更新列表时间失败: %w", err)
	}
	return nil
}

// SetPlaylistOrder 按给定顺序重排（没出现在 itemIDs 里的条目保持原顺序）。
//
// 为什么整串重写而不是「交换两个 sort_order」：整串重写是一次 UPDATE，
// 没有中间态，也不需要处理「两个条目 sort_order 相同」这种脏数据；
// 列表也就几十上百条，写一次的代价可以忽略。
func (s *Store) SetPlaylistOrder(ctx context.Context, v Viewer, id int64, itemIDs []int64) error {
	cur, err := s.GetPlaylistFor(ctx, v, id)
	if err != nil {
		return err
	}
	if !v.canEdit(cur) {
		return ErrPlaylistForbidden
	}
	if len(itemIDs) == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx,
		`update playlist_items pi
		 set sort_order = v.ord * 10
		 from (select item_id, ord from unnest($2::bigint[]) with ordinality as t(item_id, ord)) v
		 where pi.playlist_id = $1 and pi.item_id = v.item_id`, id, itemIDs); err != nil {
		return fmt.Errorf("重排列表失败: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `update playlists set updated_at = now() where id = $1`, id); err != nil {
		return fmt.Errorf("更新列表时间失败: %w", err)
	}
	return nil
}

// ListPlaylistItems 取列表里的条目（按列表顺序）。
func (s *Store) ListPlaylistItems(ctx context.Context, v Viewer, id int64, limit, offset int) ([]Item, int64, error) {
	if _, err := s.GetPlaylistFor(ctx, v, id); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	const cond = `pi.playlist_id = $1 and media_items.deleted_at is null`
	rows, err := s.pool.Query(ctx,
		`select `+itemListColumns+`
		 from media_items
		 join playlist_items pi on pi.item_id = media_items.id
		 where `+cond+`
		 order by pi.sort_order, pi.added_at, media_items.id
		 limit $2 offset $3`, id, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("查询列表条目失败: %w", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := s.pool.QueryRow(ctx,
		`select count(*) from media_items
		 join playlist_items pi on pi.item_id = media_items.id
		 where `+cond, id).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计列表条目失败: %w", err)
	}
	return items, total, nil
}

// PlaylistNeighbors 返回某个条目在列表里的前后邻居（播放器的「下一项」用）。
//
// 用 (sort_order, added_at) 的行值比较：sort_order 可能重复（历史数据或者刻意并列），
// 只比 sort_order 会漏掉或跳条。
func (s *Store) PlaylistNeighbors(ctx context.Context, v Viewer, id, itemID int64) (prev, next *int64, err error) {
	if _, err := s.GetPlaylistFor(ctx, v, id); err != nil {
		return nil, nil, err
	}
	err = s.pool.QueryRow(ctx,
		`with cur as (
		     select sort_order, added_at from playlist_items
		     where playlist_id = $1 and item_id = $2
		 )
		 select
		   (select pi.item_id from playlist_items pi, cur
		     where pi.playlist_id = $1 and (pi.sort_order, pi.added_at) < (cur.sort_order, cur.added_at)
		     order by pi.sort_order desc, pi.added_at desc, pi.item_id desc limit 1),
		   (select pi.item_id from playlist_items pi, cur
		     where pi.playlist_id = $1 and (pi.sort_order, pi.added_at) > (cur.sort_order, cur.added_at)
		     order by pi.sort_order, pi.added_at, pi.item_id limit 1)`, id, itemID).
		Scan(&prev, &next)
	if err != nil {
		return nil, nil, fmt.Errorf("查询列表邻居失败: %w", err)
	}
	return prev, next, nil
}

// PlaylistItemIndex 返回某个条目在列表里的位次（从 1 开始；不在列表里返回 0）。
func (s *Store) PlaylistItemIndex(ctx context.Context, v Viewer, id, itemID int64) (int, error) {
	if _, err := s.GetPlaylistFor(ctx, v, id); err != nil {
		return 0, err
	}
	var idx int
	if err := s.pool.QueryRow(ctx,
		`select count(*)::int from playlist_items pi
		 where pi.playlist_id = $1
		   and (pi.sort_order, pi.added_at) <= (
		         select sort_order, added_at from playlist_items where playlist_id = $1 and item_id = $2)`,
		id, itemID).Scan(&idx); err != nil {
		return 0, fmt.Errorf("查询列表位次失败: %w", err)
	}
	return idx, nil
}
