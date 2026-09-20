package match

import "testing"

// TestVariantTable 守住异体字表本身：格式必须成对，且同一个异体字不能映射到两处。
func TestVariantTable(t *testing.T) {
	for _, p := range variantProblems {
		t.Errorf("异体字表有问题: %s", p)
	}
	if len(variantFold) < 500 {
		t.Errorf("异体字表太小（%d 项），像是被误删了", len(variantFold))
	}

	// 抽查关键是日文旧字体/繁体能否折到简体
	for _, tc := range []struct {
		variant rune
		want    rune
	}{
		{'鋼', '钢'}, {'錬', '炼'}, {'術', '术'}, {'葉', '叶'},
		{'學', '学'}, {'愛', '爱'}, {'島', '岛'}, {'鐵', '铁'},
		{'發', '发'}, {'髮', '发'}, {'発', '发'}, {'髪', '发'},
	} {
		if got := variantFold[tc.variant]; got != tc.want {
			t.Errorf("variantFold[%c] = %c, 期望 %c", tc.variant, got, tc.want)
		}
	}
}
