package match

import "testing"

func TestSimilarityRealTitles(t *testing.T) {
	cases := []struct {
		a, b     string
		min, max float64
		how      string // 期望胜出的方式，空表示不校验
		why      string
	}{
		{
			a: "钢之炼金术师 FULLMETAL ALCHEMIST", b: "鋼の錬金術師 FULLMETAL ALCHEMIST",
			min: 1.0, max: 1.0, how: "equal",
			why: "本地名对 TMDB 原名：字形折叠 + 丢掉助词之后应当完全相同",
		},
		{
			a: "言叶之庭", b: "言の葉の庭",
			min: 1.0, max: 1.0, how: "equal",
			why: "丢掉两边的连接性助词（の / 之）并折掉舊字體之后完全相同",
		},
		{
			a: "致不灭的你", b: "致不灭的你",
			min: 1.0, max: 1.0, how: "equal",
			why: "完全相同",
		},
		{
			a: "冰菓", b: "冰菓 第二季",
			min: 0.70, max: 0.85, how: "contain",
			why: "包含关系给部分分，续作不该当成同一部",
		},
		{
			a: "钢之炼金术师", b: "进击的巨人",
			min: 0, max: 0.35,
			why: "完全无关的标题不能有分",
		},
		{
			a: "钢之炼金术师 FULLMETAL ALCHEMIST", b: "钢之炼金术师 FA",
			min: 0.60, max: 0.85,
			why: "同一部作品的中文标题（带 FA 后缀）：中段一致、拉丁段不同",
		},
		{
			a: "钢之炼金术师", b: "钢之炼金术师 FULLMETAL ALCHEMIST",
			min: 0.60, max: 0.80, how: "token",
			why: "一边只有中文名、一边带一长串英文：算 token 相符，不能白送满分",
		},
		{
			a: "", b: "言叶之庭",
			min: 0, max: 0, how: "empty",
			why: "任一边为空直接 0",
		},
	}

	for _, c := range cases {
		got, how := Similarity(c.a, c.b)
		if got < c.min || got > c.max {
			t.Errorf("Similarity(%q, %q) = %.3f, 期望落在 [%.2f, %.2f]（%s）",
				c.a, c.b, got, c.min, c.max, c.why)
		}
		if c.how != "" && how != c.how {
			t.Errorf("Similarity(%q, %q) 的方式 = %q, 期望 %q（%s）", c.a, c.b, how, c.how, c.why)
		}
		// 相似度必须对称，否则排序会依赖候选顺序
		if back, _ := Similarity(c.b, c.a); back != got {
			t.Errorf("Similarity 不对称: (%q,%q)=%.3f 但反向=%.3f", c.a, c.b, got, back)
		}
	}
}

func TestSimilarityNotOvergenerous(t *testing.T) {
	// 这些是真实库里容易混的：同一部作品的续作/剧场版。
	// 它们不该拿到「自动匹配」级别的分数（≥0.9）—— 注意判定能不能自动落锤
	// 还要看年份与「领先第二名多少」，这里只卡标题相似度这一项。
	pairs := [][2]string{
		{"进击的巨人", "进击的巨人 最终季"},
		{"咒术回战", "咒术回战 0 剧场版"},
		{"鬼灭之刃", "鬼灭之刃 无限列车篇"},
	}
	for _, p := range pairs {
		if got, _ := Similarity(p[0], p[1]); got >= 0.9 {
			t.Errorf("Similarity(%q, %q) = %.3f，太高了：会把续作当成同一部", p[0], p[1], got)
		}
	}
}

// TestSimilarityPrefixIsHighButAmbiguous 记录一个有意的取舍：
// 《钢之炼金术师》与《钢之炼金术师 FA》是两部不同的作品，但标题相似度本来就应该很高
// （它们是同一部作品的两次动画化），所以靠标题压不住 —— 得靠年份，
// 年份也没有时靠「领先第二名多少」。见 TestScorerNoYearNeedsMargin。
func TestSimilarityPrefixIsHighButAmbiguous(t *testing.T) {
	got, how := Similarity("钢之炼金术师", "钢之炼金术师 FA")
	if got < 0.85 {
		t.Errorf("Similarity = %.3f(%s)，低于预期：前缀扩展关系应该得很高分", got, how)
	}
	if got >= 0.999 {
		t.Errorf("Similarity = %.3f，不该判成完全相同", got)
	}
}
