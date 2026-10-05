package settings

// KeqDB：把本机的刮削结果贡献给社区元数据库（对接文档见 KeqDB 仓库的
// docs/INTEGRATION-LMBY.md）。
//
// KeqDB 是 LMBY 的**配套项目**：服务地址是内建常量（DefaultKeqDBBaseURL），
// 每个实例不需要各填一个 —— 这里只存**实例级 token**。
//
// 真正的贡献逻辑（组装 payload、传图片、提交 `/api/ingest/contribution`）
// **还没做** —— 开关与凭据先落地。
//
// Token 的规矩与 TMDB 凭据一致：加密后存库、**永不回显**（GET 只回 hasToken），
// 而且绝不能写进源码/仓库（那是实例级秘密，见对接文档 §3）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

// DefaultKeqDBBaseURL 是 KeqDB 的服务地址（配套项目，内建写死）。
//
// 将来若要指向测试环境，改这里（或加配置覆盖），不要做成让用户填的输入框。
const DefaultKeqDBBaseURL = "https://keqdb.kyarucloud.moe"

// KeqDB 是贡献目标站的凭据（明文形态，只在内存里出现）。
type KeqDB struct {
	// Token 是实例级 API token（keq_…）。空串 = 没配。
	Token  string `json:"token,omitempty"`
	FromDB bool   `json:"fromDb"`
}

// Configured 表示够不够跑贡献（地址是常量、恒有，所以看 token）。
func (k KeqDB) Configured() bool { return strings.TrimSpace(k.Token) != "" }

// KeqDBPatch 是设置页提交的补丁（nil = 不改，指向空串 = 清掉）。
//
// 与 TMDB 的 Patch 同理：token 不可能回显，所以「没填」与「要清空」必须能区分。
type KeqDBPatch struct {
	Token *string
}

// KeqDB 读当前凭据（token 解密后返回）。
func (s *Service) KeqDB(ctx context.Context) (KeqDB, error) {
	raw, err := s.st.GetKeqDBConfig(ctx)
	if err != nil {
		return KeqDB{}, err
	}
	out := KeqDB{}
	if raw.Token == "" {
		return out, nil
	}
	out.FromDB = true
	if s.cipher == nil {
		out.Token = raw.Token
		return out, nil
	}
	if out.Token, err = s.cipher.Open(raw.Token); err != nil {
		return out, fmt.Errorf("解密 KeqDB 实例 token 失败: %w", err)
	}
	return out, nil
}

// ApplyKeqDB 应用一次补丁：合并、加密 token、写库。
func (s *Service) ApplyKeqDB(ctx context.Context, patch KeqDBPatch) (KeqDB, error) {
	cur, err := s.KeqDB(ctx)
	if err != nil {
		return KeqDB{}, err
	}
	if patch.Token != nil {
		cur.Token = strings.TrimSpace(*patch.Token)
	}

	sealed := cur.Token
	if s.cipher != nil {
		if sealed, err = s.cipher.Seal(cur.Token); err != nil {
			return KeqDB{}, fmt.Errorf("加密 KeqDB 实例 token 失败: %w", err)
		}
	}
	if err := s.st.SetKeqDBConfig(ctx, store.KeqDBConfig{Token: sealed}); err != nil {
		return KeqDB{}, err
	}
	cur.FromDB = true
	s.log.Info("已更新 KeqDB 共享凭据", "hasToken", cur.Token != "")
	return cur, nil
}
