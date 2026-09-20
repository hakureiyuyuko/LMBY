package match

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/provider"
)

// 判定结果。
const (
	// DecisionAuto 分数够高、且明显领先第二名 —— 可以直接落库，不用人看。
	DecisionAuto = "auto"
	// DecisionReview 有希望但不够确定 —— 进人工匹配队列。
	DecisionReview = "review"
	// DecisionReject 基本不可能 —— 不展示给用户。
	DecisionReject = "reject"
)

// Local 是本地库里的一个条目，字段来自扫描结果与 nfo 导入。
//
// 除了标题与年份，还带上剧集结构信号（季号、集数、集时长），
// 因为「同名同年的两部剧集」只能靠结构分辨 ——
// 《钢之炼金术师》2003（51 集）与《钢之炼金术师 FA》2009（64 集）就是典型。
type Local struct {
	Kind            string // provider.KindMovie / KindTV，空表示不限
	Title           string
	OriginalTitle   string
	Aliases         []string
	Year            int
	SeasonNumber    int
	EpisodeCount    int
	RuntimesSeconds []int
}

// SeasonEpisodes 是候选剧集某一季的集数（来自详情接口的 seasons 数组）。
type SeasonEpisodes struct {
	Season   int
	Episodes int
}

// Candidate 是提供方返回的一个候选，字段尽量与 provider.SearchResult 对齐。
type Candidate struct {
	ID             int
	Kind           string
	Title          string
	OriginalTitle  string
	AltTitles      []string
	Year           int
	ReleaseDate    string
	Popularity     float64
	VoteCount      int
	Rating         float64
	SeasonEpisodes []SeasonEpisodes // 需要取详情才有，可选
	RuntimeMinutes int              // 电影时长或单集典型时长，可选
}

// FromSearch 把提供方的搜索结果转成候选（省去调用方逐字段搬运）。
func FromSearch(r provider.SearchResult) Candidate {
	return Candidate{
		ID:            r.ID,
		Kind:          r.Kind,
		Title:         r.Title,
		OriginalTitle: r.OriginalTitle,
		Year:          r.Year,
		ReleaseDate:   r.ReleaseDate,
		Popularity:    r.Popularity,
		VoteCount:     r.VoteCount,
		Rating:        r.Rating,
	}
}

// Part 是一项打分明细。Weight 是声明权重，最终总分按「实际参与的项」重新归一化，
// 所以本地没有年份时，年份那一项直接不参与，不会把总分拉低。
type Part struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Score  float64 `json:"score"`
	Note   string  `json:"note"`
}

// Verdict 是一条候选的匹配结论。
type Verdict struct {
	CandidateID  int     `json:"candidateId"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	Year         int     `json:"year"`
	Score        float64 `json:"score"`
	Decision     string  `json:"decision"`
	Parts        []Part  `json:"parts"`
	MatchedAlias string  `json:"matchedAlias,omitempty"` // 靠别名/原名命中时记下命中的那条标题

	// 下面是候选的原始信息，方便界面与 CLI 直接展示，也用作排序的决胜项。
	Popularity    float64 `json:"popularity,omitempty"`
	VoteCount     int     `json:"voteCount,omitempty"`
	RunnerUpScore float64 `json:"runnerUpScore,omitempty"` // 只在榜首上有值
	Margin        float64 `json:"margin,omitempty"`
}

// Part 按名称取一项明细。
func (v Verdict) Part(name string) (Part, bool) {
	for _, p := range v.Parts {
		if p.Name == name {
			return p, true
		}
	}
	return Part{}, false
}

// Weights 是三项信号的权重（相对值，不需要加起来等于 1）。
type Weights struct {
	Title     float64
	Year      float64
	Structure float64
}

// Scorer 是打分器。零值可用（会补齐下面这套默认值）。
type Scorer struct {
	// AutoThreshold 以上直接自动匹配。
	AutoThreshold float64
	// ReviewThreshold 以上进人工队列，以下丢弃。
	ReviewThreshold float64
	// Margin 是自动匹配额外要求的「领先第二名的幅度」。
	// 同名重制版（《钢之炼金术师》2003 / FA 2009）光看绝对分容易选错。
	// 0.12 是拿实测语料卡出来的：真正不同的作品之间分差通常 >0.25，
	// 而「本体 vs FA/续作」这类危险对只差 0.10 上下 —— 正好要被挡住。
	Margin  float64
	Weights Weights
}

// DefaultScorer 返回经验默认值。
//
// 阈值的取法：标题 0.70 + 年份 0.20 + 结构 0.10。
// 一个「标题勉强像（0.75）但年份对得上」的候选 ≈ 0.78，进人工；
// 「标题很像（0.9）年份也对」≈ 0.92，自动。
func DefaultScorer() Scorer {
	return Scorer{
		AutoThreshold:   0.85,
		ReviewThreshold: 0.50,
		Margin:          0.12,
		Weights:         Weights{Title: 0.70, Year: 0.20, Structure: 0.10},
	}
}

// normalized 补齐零值，让零值 Scorer 也能直接用。
func (s Scorer) normalized() Scorer {
	d := DefaultScorer()
	if s.AutoThreshold <= 0 {
		s.AutoThreshold = d.AutoThreshold
	}
	if s.ReviewThreshold <= 0 {
		s.ReviewThreshold = d.ReviewThreshold
	}
	if s.Margin < 0 {
		s.Margin = d.Margin
	}
	if s.Weights == (Weights{}) {
		s.Weights = d.Weights
	}
	return s
}

// Score 给单条候选打分（不考虑与其它候选的相对关系）。
func (s Scorer) Score(l Local, c Candidate) Verdict {
	s = s.normalized()
	v := Verdict{
		CandidateID: c.ID,
		Kind:        c.Kind,
		Title:       c.Title,
		Year:        c.Year,
		Popularity:  c.Popularity,
		VoteCount:   c.VoteCount,
	}

	if l.Kind != "" && c.Kind != "" && l.Kind != c.Kind {
		v.Decision = DecisionReject
		v.Parts = []Part{{
			Name: "kind",
			Note: fmt.Sprintf("类型不符：本地 %s / 候选 %s", l.Kind, c.Kind),
		}}
		return v
	}

	var parts []Part
	if p, alias, ok := s.titlePart(l, c); ok {
		p.Weight = s.Weights.Title
		parts = append(parts, p)
		v.MatchedAlias = alias
	}
	if p, ok := yearPart(l, c); ok {
		p.Weight = s.Weights.Year
		parts = append(parts, p)
	}
	if p, ok := structurePart(l, c); ok {
		p.Weight = s.Weights.Structure
		parts = append(parts, p)
	}

	var sum, weight float64
	for _, p := range parts {
		sum += p.Weight * p.Score
		weight += p.Weight
	}
	if weight > 0 {
		v.Score = round3(sum / weight)
	}
	v.Parts = parts
	v.Decision = s.decide(v.Score)
	return v
}

// Rank 给一批候选打分排序，并决定榜首是自动还是进人工。
//
// 排序规则：分数降序，再按热度 / 投票数 / id —— 全部确定，方便测试与复现。
func (s Scorer) Rank(l Local, cands []Candidate) []Verdict {
	s = s.normalized()

	out := make([]Verdict, 0, len(cands))
	for _, c := range cands {
		if l.Kind != "" && c.Kind != "" && l.Kind != c.Kind {
			continue
		}
		out = append(out, s.Score(l, c))
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Popularity != b.Popularity {
			return a.Popularity > b.Popularity
		}
		if a.VoteCount != b.VoteCount {
			return a.VoteCount > b.VoteCount
		}
		return a.CandidateID < b.CandidateID
	})

	if len(out) > 0 {
		top := &out[0]
		runner := 0.0
		if len(out) > 1 {
			runner = out[1].Score
		}
		top.RunnerUpScore = runner
		top.Margin = round3(top.Score - runner)
		// 榜首的最终判定要额外看领先幅度
		switch {
		case top.Score >= s.AutoThreshold && top.Margin >= s.Margin:
			top.Decision = DecisionAuto
		case top.Score >= s.ReviewThreshold:
			top.Decision = DecisionReview
		default:
			top.Decision = DecisionReject
		}
	}
	return out
}

// Best 返回排名第一的结论（没有候选时 ok=false）。
func (s Scorer) Best(l Local, cands []Candidate) (Verdict, bool) {
	ranked := s.Rank(l, cands)
	if len(ranked) == 0 {
		return Verdict{}, false
	}
	return ranked[0], true
}

func (s Scorer) decide(score float64) string {
	switch {
	case score >= s.AutoThreshold:
		return DecisionAuto
	case score >= s.ReviewThreshold:
		return DecisionReview
	default:
		return DecisionReject
	}
}

// titlePart 取「本地所有标题（含别名）」×「候选所有标题（含原名/别名）」里最像的一对。
//
// 只取最大值不会重复计分；命中的那条如果不是主标题，会记进 MatchedAlias，
// 界面就能显示「靠别名命中的」—— 这正是《钢之炼金术师 FULLMETAL ALCHEMIST》
// 靠原名《鋼の錬金術師 FULLMETAL ALCHEMIST》命中的情形。
func (s Scorer) titlePart(l Local, c Candidate) (Part, string, bool) {
	locals := collectTitles(append([]string{l.Title, l.OriginalTitle}, l.Aliases...)...)
	cands := collectTitles(append([]string{c.Title, c.OriginalTitle}, c.AltTitles...)...)
	if len(locals) == 0 || len(cands) == 0 {
		return Part{}, "", false
	}

	best := -1.0
	note, alias := "", ""
	for _, a := range locals {
		for _, b := range cands {
			sc, how := Similarity(a, b)
			if sc <= best {
				continue
			}
			best = sc
			alias = ""
			if a != l.Title || b != c.Title {
				alias = b
			}
			if sc >= 0.999 {
				note = fmt.Sprintf("《%s》与《%s》完全一致", cleanupTitle(a), cleanupTitle(b))
			} else {
				note = fmt.Sprintf("《%s》≈《%s》 %.2f(%s)", cleanupTitle(a), cleanupTitle(b), sc, how)
			}
		}
	}
	if best < 0 {
		best = 0
	}
	return Part{Name: "title", Score: best, Note: note}, alias, true
}

// yearPart 对照年份。差 1 年给部分分 —— 跨年播出的动画（10 月番）在
// TMDB 与本地命名里经常差一岁，直接判 0 会误杀。
func yearPart(l Local, c Candidate) (Part, bool) {
	if l.Year <= 0 || c.Year <= 0 {
		// 任一边没有年份信息，这一项就不参与（不要因为「甲方没写」而扣分）
		return Part{}, false
	}
	diff := abs(l.Year - c.Year)
	var score float64
	switch {
	case diff == 0:
		score = 1
	case diff == 1:
		score = 0.55
	case diff == 2:
		score = 0.25
	default:
		score = 0
	}
	note := fmt.Sprintf("年份一致 %d", l.Year)
	if diff > 0 {
		note = fmt.Sprintf("年份差 %d 年（本地 %d / 候选 %d）", diff, l.Year, c.Year)
	}
	return Part{Name: "year", Score: score, Note: note}, true
}

// structurePart 对照剧集结构：集数与集时长。
//
// 集数允许有出入（TMDB 的特别篇计数与本地目录常常不一致），所以是分档给分，
// 不用「必须相等」；时长则是很好用的交叉验证 ——
// 同名的剧场版与 TV 版时长差得远，这一项立刻能区分开。
func structurePart(l Local, c Candidate) (Part, bool) {
	var scores []float64
	var notes []string

	if l.EpisodeCount > 0 {
		if n, ok := seasonEpisodeCount(l, c); ok {
			sc := ratioScore(l.EpisodeCount, n)
			scores = append(scores, sc)
			notes = append(notes, fmt.Sprintf("集数 %d vs %d → %.2f", l.EpisodeCount, n, sc))
		}
	}
	if mins := medianMinutes(l.RuntimesSeconds); mins > 0 && c.RuntimeMinutes > 0 {
		diff := abs(mins - c.RuntimeMinutes)
		var sc float64
		switch {
		case diff <= 1:
			sc = 1
		case diff <= 3:
			sc = 0.7
		case diff <= 6:
			sc = 0.4
		default:
			sc = 0.15
		}
		scores = append(scores, sc)
		notes = append(notes, fmt.Sprintf("时长 %d vs %d 分钟 → %.2f", mins, c.RuntimeMinutes, sc))
	}

	if len(scores) == 0 {
		return Part{}, false
	}
	var sum float64
	for _, sc := range scores {
		sum += sc
	}
	return Part{
		Name:  "structure",
		Score: round3(sum / float64(len(scores))),
		Note:  strings.Join(notes, "；"),
	}, true
}

// seasonEpisodeCount 取候选里与本地季号对应的集数；本地没写季号就取总数。
func seasonEpisodeCount(l Local, c Candidate) (int, bool) {
	if len(c.SeasonEpisodes) == 0 {
		return 0, false
	}
	if l.SeasonNumber > 0 {
		for _, se := range c.SeasonEpisodes {
			if se.Season == l.SeasonNumber && se.Episodes > 0 {
				return se.Episodes, true
			}
		}
		if len(c.SeasonEpisodes) == 1 && c.SeasonEpisodes[0].Episodes > 0 {
			return c.SeasonEpisodes[0].Episodes, true
		}
		return 0, false
	}
	total := 0
	for _, se := range c.SeasonEpisodes {
		total += se.Episodes
	}
	if total == 0 {
		return 0, false
	}
	return total, true
}

func ratioScore(a, b int) float64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	switch r := float64(lo) / float64(hi); {
	case r >= 0.98:
		return 1
	case r >= 0.85:
		return 0.7
	case r >= 0.6:
		return 0.45
	default:
		return 0.15
	}
}

// medianMinutes 取集时长（秒）的中位数并换算成分钟，用来躲开片头/特典的离群值。
func medianMinutes(seconds []int) int {
	vals := make([]int, 0, len(seconds))
	for _, s := range seconds {
		if s > 0 {
			vals = append(vals, s)
		}
	}
	if len(vals) == 0 {
		return 0
	}
	sort.Ints(vals)
	return int(math.Round(float64(vals[len(vals)/2]) / 60))
}

// collectTitles 收集非空标题并去重。
func collectTitles(titles ...string) []string {
	seen := make(map[string]struct{}, len(titles))
	out := make([]string, 0, len(titles))
	for _, t := range titles {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func round3(f float64) float64 {
	return math.Round(f*1000) / 1000
}
