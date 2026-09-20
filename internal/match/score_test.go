package match

import (
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/provider"
)

// 本文件里的语料来自 2026-09-20 对真实 TMDB API 的实测
// （记录见 docs/ROADMAP.md 的 M2 一节），不是编出来的：
// 三条真实命中 —— 《钢之炼金术师 FULLMETAL ALCHEMIST》(2009) → 31911、
// 《致不灭的你》(2021) → 97525、《言叶之庭》(2013) → 198375。
//
// 它们同时是刮削器「自动匹配准确率」验收要过的样本。

func TestScorerAlchemyRealCase(t *testing.T) {
	local := Local{
		Kind:            provider.KindTV,
		Title:           "钢之炼金术师 FULLMETAL ALCHEMIST",
		Year:            2009,
		SeasonNumber:    1,
		EpisodeCount:    64,
		RuntimesSeconds: repeatInt(24*60, 64),
	}
	cands := []Candidate{
		{ // 正主：TMDB 31911《钢之炼金术师 FA》
			ID:             31911,
			Kind:           provider.KindTV,
			Title:          "钢之炼金术师 FA",
			OriginalTitle:  "鋼の錬金術師 FULLMETAL ALCHEMIST",
			Year:           2009,
			Popularity:     120,
			VoteCount:      2586,
			Rating:         8.71,
			SeasonEpisodes: []SeasonEpisodes{{Season: 0, Episodes: 20}, {Season: 1, Episodes: 64}},
		},
		{ // 危险条目：《钢之炼金术师》2003 版 —— 同名、同作者、容易被误配
			ID:             31910,
			Kind:           provider.KindTV,
			Title:          "钢之炼金术师",
			OriginalTitle:  "鋼の錬金術師",
			Year:           2003,
			Popularity:     80,
			SeasonEpisodes: []SeasonEpisodes{{Season: 1, Episodes: 51}},
		},
	}

	ranked := DefaultScorer().Rank(local, cands)
	if len(ranked) != 2 {
		t.Fatalf("候选数 = %d, 期望 2", len(ranked))
	}

	top := ranked[0]
	if top.CandidateID != 31911 {
		t.Errorf("榜首 = %d, 期望 31911（2009 版 FA）", top.CandidateID)
	}
	if top.Decision != DecisionAuto {
		t.Errorf("榜首判定 = %s, 期望 %s（明细: %s）", top.Decision, DecisionAuto, partsSummary(top))
	}
	if top.Score < 0.90 {
		t.Errorf("榜首分数 = %.3f, 期望 ≥0.90", top.Score)
	}
	if top.Margin < 0.10 {
		t.Errorf("领先幅度 = %.3f, 期望 ≥0.10（对 2003 版必须拉开差距）", top.Margin)
	}
	if top.MatchedAlias == "" {
		t.Error("应当记录「靠原名命中」，MatchedAlias 却是空的")
	}
	if ranked[1].Decision == DecisionAuto {
		t.Errorf("2003 版竟然也被判为自动匹配（分数 %.3f）", ranked[1].Score)
	}
	// 年份这一项必须真的参与了，而且 2003 版要被打到 0 分
	if p, ok := ranked[1].Part("year"); !ok || p.Score != 0 {
		t.Errorf("2003 版的年份项 = %+v, 期望存在且为 0 分", p)
	}
	t.Logf("榜首 %d 分 %.3f（领先 %.3f）；2003 版 %.3f", top.CandidateID, top.Score, top.Margin, ranked[1].Score)
}

func TestScorerFushiRealCase(t *testing.T) {
	local := Local{
		Kind:         provider.KindTV,
		Title:        "致不灭的你",
		Year:         2021,
		SeasonNumber: 1,
		EpisodeCount: 20,
	}
	cands := []Candidate{
		{
			ID: 97525, Kind: provider.KindTV,
			Title: "致不灭的你", OriginalTitle: "不滅のあなたへ",
			Year: 2021, Popularity: 60, VoteCount: 300,
			SeasonEpisodes: []SeasonEpisodes{{Season: 1, Episodes: 20}},
		},
		{ // 无关候选
			ID: 1429, Kind: provider.KindTV,
			Title: "进击的巨人", OriginalTitle: "進撃の巨人",
			Year: 2013, Popularity: 400, VoteCount: 9000,
		},
	}

	ranked := DefaultScorer().Rank(local, cands)
	if ranked[0].CandidateID != 97525 {
		t.Fatalf("榜首 = %d, 期望 97525", ranked[0].CandidateID)
	}
	if ranked[0].Decision != DecisionAuto {
		t.Errorf("判定 = %s, 期望 %s（明细: %s）", ranked[0].Decision, DecisionAuto, partsSummary(ranked[0]))
	}
	if ranked[1].Decision != DecisionReject {
		t.Errorf("无关候选判定 = %s, 期望 %s（分数 %.3f）", ranked[1].Decision, DecisionReject, ranked[1].Score)
	}
}

func TestScorerGardenOfWordsRealCase(t *testing.T) {
	local := Local{
		Kind:            provider.KindMovie,
		Title:           "言叶之庭",
		Year:            2013,
		RuntimesSeconds: []int{46 * 60},
	}
	cands := []Candidate{
		{
			ID: 198375, Kind: provider.KindMovie,
			Title: "言叶之庭", OriginalTitle: "言の葉の庭",
			Year: 2013, RuntimeMinutes: 46, Popularity: 40, VoteCount: 1900,
		},
		{ // 同导演的另一部，年份不符
			ID: 14650, Kind: provider.KindMovie,
			Title: "秒速5厘米", OriginalTitle: "秒速5センチメートル",
			Year: 2007, RuntimeMinutes: 63, Popularity: 90,
		},
	}

	ranked := DefaultScorer().Rank(local, cands)
	if ranked[0].CandidateID != 198375 {
		t.Fatalf("榜首 = %d, 期望 198375", ranked[0].CandidateID)
	}
	if ranked[0].Decision != DecisionAuto {
		t.Errorf("判定 = %s, 期望 %s（明细: %s）", ranked[0].Decision, DecisionAuto, partsSummary(ranked[0]))
	}
	if ranked[0].Score < 0.99 {
		t.Errorf("分数 = %.3f, 期望接近满分（标题与年份都对上）", ranked[0].Score)
	}
	if ranked[1].Decision != DecisionReject {
		t.Errorf("《秒速5厘米》判定 = %s, 期望 %s（分数 %.3f）",
			ranked[1].Decision, DecisionReject, ranked[1].Score)
	}
}

// TestScorerNoYearNeedsMargin 是这套设计里最重要的一条安全网：
// 本地没有年份、候选里有两个几乎同名的条目时，不许自动落锤。
func TestScorerNoYearNeedsMargin(t *testing.T) {
	local := Local{Kind: provider.KindTV, Title: "钢之炼金术师"}
	cands := []Candidate{
		{ID: 31910, Kind: provider.KindTV, Title: "钢之炼金术师", Year: 2003},
		{ID: 31911, Kind: provider.KindTV, Title: "钢之炼金术师 FA", Year: 2009},
	}

	top := DefaultScorer().Rank(local, cands)[0]
	if top.CandidateID != 31910 {
		t.Errorf("榜首 = %d, 期望 31910（标题完全相同的那条）", top.CandidateID)
	}
	if top.Decision == DecisionAuto {
		t.Errorf("判定 = %s：标题近乎相同、又没有年份可辨，不该自动落锤（领先只有 %.3f）",
			top.Decision, top.Margin)
	}
	if top.Margin >= DefaultScorer().Margin {
		t.Errorf("领先幅度 = %.3f, 期望小于阈值 %.2f", top.Margin, DefaultScorer().Margin)
	}
}

// TestScorerStructureBreaksTie 验证「集数」真的在起作用：
// 标题同样像、年份相同，靠集数把正确的那条顶上来。
func TestScorerStructureBreaksTie(t *testing.T) {
	local := Local{
		Kind: provider.KindTV, Title: "某作品", Year: 2020,
		SeasonNumber: 1, EpisodeCount: 24,
	}
	cands := []Candidate{
		{ID: 1, Kind: provider.KindTV, Title: "某作品", Year: 2020, SeasonEpisodes: []SeasonEpisodes{{Season: 1, Episodes: 12}}},
		{ID: 2, Kind: provider.KindTV, Title: "某作品", Year: 2020, SeasonEpisodes: []SeasonEpisodes{{Season: 1, Episodes: 24}}},
	}

	ranked := DefaultScorer().Rank(local, cands)
	if ranked[0].CandidateID != 2 {
		t.Errorf("榜首 = %d, 期望 2（集数 24 与本地一致）", ranked[0].CandidateID)
	}
	if p, ok := ranked[1].Part("structure"); !ok || p.Score >= 0.5 {
		t.Errorf("集数 12 的那条结构项 = %+v, 期望存在且明显偏低", p)
	}
}

func TestScorerAliasHit(t *testing.T) {
	local := Local{
		Kind:  provider.KindMovie,
		Title: "The Garden of Words",
		Aliases: []string{
			"言叶之庭",
		},
		Year: 2013,
	}
	cand := Candidate{
		ID: 198375, Kind: provider.KindMovie,
		Title: "言の葉の庭", Year: 2013, RuntimeMinutes: 46,
	}

	v := DefaultScorer().Score(local, cand)
	if v.Score < 0.9 {
		t.Errorf("分数 = %.3f, 期望靠别名命中拿高分（明细: %s）", v.Score, partsSummary(v))
	}
	if v.MatchedAlias == "" {
		t.Error("应当记录靠别名命中")
	}
}

func TestScorerKindMismatch(t *testing.T) {
	local := Local{Kind: provider.KindMovie, Title: "言叶之庭", Year: 2013}
	cands := []Candidate{
		{ID: 198375, Kind: provider.KindTV, Title: "言叶之庭", Year: 2013},
		{ID: 198375, Kind: provider.KindMovie, Title: "言叶之庭", Year: 2013},
	}

	ranked := DefaultScorer().Rank(local, cands)
	if len(ranked) != 1 {
		t.Fatalf("候选数 = %d, 期望 1（类型不符的应当被过滤掉）", len(ranked))
	}
	if ranked[0].Kind != provider.KindMovie {
		t.Errorf("剩下的类型 = %s, 期望 %s", ranked[0].Kind, provider.KindMovie)
	}

	// 直接调 Score 时也要拒绝，而不是给出一个能用的分数
	if v := DefaultScorer().Score(local, cands[0]); v.Decision != DecisionReject || v.Score != 0 {
		t.Errorf("类型不符时 Score = %.3f/%s, 期望 0/%s", v.Score, v.Decision, DecisionReject)
	}
}

func TestScorerTraditionalLocalTitle(t *testing.T) {
	// 本地库用繁体命名时也要能配上
	local := Local{Kind: provider.KindTV, Title: "鋼之錬金術師 FULLMETAL ALCHEMIST", Year: 2009}
	cand := Candidate{
		ID: 31911, Kind: provider.KindTV,
		Title: "钢之炼金术师 FA", OriginalTitle: "鋼の錬金術師 FULLMETAL ALCHEMIST", Year: 2009,
	}

	v := DefaultScorer().Score(local, cand)
	if v.Score < 0.9 {
		t.Errorf("分数 = %.3f, 期望 ≥0.9（繁体本地名对原名）", v.Score)
	}
}

func TestScorerZeroValueAndCustomThresholds(t *testing.T) {
	local := Local{Kind: provider.KindMovie, Title: "言叶之庭"}
	cand := Candidate{ID: 1, Kind: provider.KindMovie, Title: "言叶之庭"}

	// 零值 Scorer 必须可用（会自己补默认值）
	var zero Scorer
	if v := zero.Score(local, cand); v.Score != 1 || v.Decision != DecisionAuto {
		t.Errorf("零值 Scorer: %.3f/%s, 期望 1/%s", v.Score, v.Decision, DecisionAuto)
	}

	// 自定义阈值要生效
	strict := Scorer{AutoThreshold: 1.0, ReviewThreshold: 0.9}
	if v := strict.Score(local, cand); v.Decision != DecisionAuto {
		t.Errorf("阈值 1.0 且分数 1.0 时判定 = %s, 期望 %s", v.Decision, DecisionAuto)
	}
	strict.AutoThreshold = 1.01
	if v := strict.Rank(local, []Candidate{cand})[0]; v.Decision != DecisionReview {
		t.Errorf("阈值 1.01 时判定 = %s, 期望 %s", v.Decision, DecisionReview)
	}
}

func TestScorePartsAlwaysExplain(t *testing.T) {
	// 每一项明细都必须带说明文字，界面要拿它解释「为什么是这个分」
	local := Local{
		Kind: provider.KindTV, Title: "钢之炼金术师 FULLMETAL ALCHEMIST", Year: 2009,
		SeasonNumber: 1, EpisodeCount: 64, RuntimesSeconds: []int{24 * 60},
	}
	cand := Candidate{
		ID: 31911, Kind: provider.KindTV,
		Title: "钢之炼金术师 FA", OriginalTitle: "鋼の錬金術師 FULLMETAL ALCHEMIST", Year: 2009,
		RuntimeMinutes: 24, SeasonEpisodes: []SeasonEpisodes{{Season: 1, Episodes: 64}},
	}

	v := DefaultScorer().Score(local, cand)
	if len(v.Parts) != 3 {
		t.Fatalf("明细项数 = %d, 期望 3（标题/年份/结构）", len(v.Parts))
	}
	var weightSum float64
	for _, p := range v.Parts {
		if p.Name == "" || p.Note == "" {
			t.Errorf("明细项缺名称或说明: %+v", p)
		}
		if p.Weight <= 0 {
			t.Errorf("明细项 %s 的权重 = %.2f, 期望 >0", p.Name, p.Weight)
		}
		weightSum += p.Weight
	}
	if weightSum < 0.999 || weightSum > 1.001 {
		t.Errorf("默认权重之和 = %.4f, 期望 1.0（总分按参与项归一化，权重应当配得刚好）", weightSum)
	}
}

func TestFromSearch(t *testing.T) {
	r := provider.SearchResult{
		ID: 31911, Kind: provider.KindTV,
		Title: "钢之炼金术师 FA", OriginalTitle: "鋼の錬金術師 FULLMETAL ALCHEMIST",
		Year: 2009, ReleaseDate: "2009-04-05", Popularity: 120, VoteCount: 2586, Rating: 8.71,
	}
	c := FromSearch(r)
	if c.ID != r.ID || c.Title != r.Title || c.OriginalTitle != r.OriginalTitle ||
		c.Year != r.Year || c.Kind != r.Kind || c.Popularity != r.Popularity ||
		c.VoteCount != r.VoteCount || c.Rating != r.Rating || c.ReleaseDate != r.ReleaseDate {
		t.Errorf("FromSearch 丢字段: %+v", c)
	}
}

// —— 小工具 ——

func repeatInt(v, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func partsSummary(v Verdict) string {
	s := ""
	for i, p := range v.Parts {
		if i > 0 {
			s += " | "
		}
		s += p.Name + ": " + p.Note
	}
	return s
}
