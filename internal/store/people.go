package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// 本文件是演职员（people + item_people，迁移 0010）。
//
// 数据来源目前只有 **nfo**：`<actor>`（带 role/type/tmdbid…）、`<director>`、
// `<credits>`。刮削侧的 TMDB credits 还没接 —— 也就是说「本地没有 nfo 的条目
// 演职员是空的」，这一点在界面与文档里都如实写出来。

// ItemPerson 是条目的一位演职员。
type ItemPerson struct {
	PersonID    int64             `json:"personId"`
	Name        string            `json:"name"`
	Role        string            `json:"role"`
	Character   string            `json:"character,omitempty"`
	Order       int               `json:"order"`
	ProviderIDs map[string]string `json:"providerIds,omitempty"`
}

// personDedupeKey 算「一个人」的去重键。
//
// 有外部 id 就按外部 id（同名不同人是真的存在，尤其日文/中文译名）；
// 三个都没有才退化成「去掉所有空白 + 小写」的名字。
func personDedupeKey(name string, ids map[string]string) string {
	for _, k := range []string{"tmdb", "imdb", "tvdb"} {
		if v := strings.TrimSpace(ids[k]); v != "" {
			return k + ":" + v
		}
	}
	return "name:" + strings.ToLower(strings.Join(strings.Fields(name), ""))
}

// ReplaceItemPeople 整体替换一个条目的演职员表。
//
// 为什么是「整体替换」而不是逐条合并：一次扫描读到的就是这份 nfo 的全部演职员，
// 合并反而会把 nfo 里已经删掉的演员留在库里。**空列表 = 清空** ——
// 调用方要自己决定「nfo 里没有演职员」时到底该不该清（扫描器选择：不清）。
func (s *Store) ReplaceItemPeople(ctx context.Context, itemID int64, people []ItemPerson) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("写入演职员失败: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `delete from item_people where item_id = $1`, itemID); err != nil {
		return fmt.Errorf("清掉旧演职员失败: %w", err)
	}

	for i, p := range people {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		role := strings.TrimSpace(p.Role)
		if role == "" {
			role = "actor"
		}
		ids := p.ProviderIDs
		if ids == nil {
			ids = map[string]string{}
		}
		raw, err := json.Marshal(ids)
		if err != nil {
			return fmt.Errorf("编码人员外部 id 失败: %w", err)
		}

		var personID int64
		if err := tx.QueryRow(ctx,
			`insert into people (name, provider_ids, dedupe_key)
			 values ($1, $2::jsonb, $3)
			 on conflict (dedupe_key) do update
			   set name = excluded.name, provider_ids = excluded.provider_ids, updated_at = now()
			 returning id`,
			name, string(raw), personDedupeKey(name, ids)).Scan(&personID); err != nil {
			return fmt.Errorf("写入人员失败: %w", err)
		}

		order := p.Order
		if order == 0 {
			order = i
		}
		if _, err := tx.Exec(ctx,
			`insert into item_people (item_id, person_id, role, character, sort_order)
			 values ($1, $2, $3, $4, $5)
			 on conflict (item_id, person_id, role) do update
			   set character = excluded.character, sort_order = excluded.sort_order`,
			itemID, personID, role, strings.TrimSpace(p.Character), order); err != nil {
			return fmt.Errorf("写入条目与人员的关系失败: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("写入演职员失败: %w", err)
	}
	return nil
}

// ListItemPeople 列出条目的演职员：演员在前（按 nfo 里的顺序），幕后在后。
//
// 注意 role 的比较用 lower()：nfo 里写的是 `Actor` / `GuestStar` / `Director`
// （大写开头，照原样落库，不丢信息），直接与 'actor' 比会一个都不匹配 ——
// 那样排序就静默退化成「全按 sort_order」，导演会插在演员中间（实测踩到）。
func (s *Store) ListItemPeople(ctx context.Context, itemID int64) ([]ItemPerson, error) {
	rows, err := s.pool.Query(ctx,
		`select p.id, p.name, ip.role, ip.character, ip.sort_order, p.provider_ids
		 from item_people ip
		 join people p on p.id = ip.person_id
		 where ip.item_id = $1
		 order by (lower(ip.role) = 'actor') desc, ip.sort_order, p.name`, itemID)
	if err != nil {
		return nil, fmt.Errorf("查询演职员失败: %w", err)
	}
	defer rows.Close()

	out := []ItemPerson{}
	for rows.Next() {
		var p ItemPerson
		var ids map[string]string
		if err := rows.Scan(&p.PersonID, &p.Name, &p.Role, &p.Character, &p.Order, &ids); err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			p.ProviderIDs = ids
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPeople 统计库里的人与关系数（验收/自检用）。
func (s *Store) CountPeople(ctx context.Context) (people, links int, err error) {
	err = s.pool.QueryRow(ctx,
		`select (select count(*) from people)::int,
		        (select count(*) from item_people)::int`).Scan(&people, &links)
	if err != nil {
		return 0, 0, fmt.Errorf("统计演职员失败: %w", err)
	}
	return people, links, nil
}
