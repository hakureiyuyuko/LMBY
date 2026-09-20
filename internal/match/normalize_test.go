package match

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		why  string
	}{
		{"言の葉の庭", "言叶庭", "日文原名与中文译名的连接性助词都丢掉：の / 之"},
		{"言叶之庭", "言叶庭", "中文译名的「之」也当噪声"},
		{"鋼の錬金術師 FULLMETAL ALCHEMIST", "钢炼金术师 fullmetal alchemist", "繁体/日文旧字体 + 拉丁分词"},
		{"钢之炼金术师 FULLMETAL ALCHEMIST", "钢炼金术师 fullmetal alchemist", "（真实案例）与上面的日文原名折成完全相同的字符串"},
		{"ソードアート・オンラインⅡ", "ソードアート オンラインii", "中点当分隔符、罗马数字折成拉丁字母"},
		{"Love Live! 虹ヶ咲学園スクールアイドル同好会", "love live 虹ヶ咲学园スクールアイドル同好会", "大小写折叠、学園→学园"},
		{"魔卡少女樱 ①", "魔卡少女樱 1", "带圈数字"},
		{"进击的巨人 最终季（前篇）", "进击巨人 最终季 前篇", "全角括号当分隔符，同时丢掉助词 的"},
		{"Fate/stay night", "fate stay night", "斜杠当分隔符"},
		{"  THE   IDOLM@STER  ", "the idolm ster", "连续空白折叠、@ 变分隔符"},
		{"", "", "空串"},
		{"！！!", "", "纯标点折成空串"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, 期望 %q（%s）", c.in, got, c.want, c.why)
		}
	}
}

func TestCompact(t *testing.T) {
	if got, want := Compact("钢之炼金术师 FULLMETAL ALCHEMIST"), "钢炼金术师fullmetalalchemist"; got != want {
		t.Errorf("Compact() = %q, 期望 %q", got, want)
	}
}

func TestTokensOf(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// 中文逐字成 token，拉丁按词成 token；连接性助词被丢掉
		{"钢之炼金术师 fullmetal alchemist", []string{"钢", "炼", "金", "术", "师", "fullmetal", "alchemist"}},
		{"h264 10bit", []string{"h264", "10bit"}},
		{"", nil},
	}
	for _, c := range cases {
		got := tokensOf(Normalize(c.in))
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokensOf(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}
