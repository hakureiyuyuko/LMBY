package scanner

import "testing"

func i32(v int32) *int32 { return &v }

// TestScanKey 守住身份串的构造：与内存去重键同构，空标题不产生身份。
func TestScanKey(t *testing.T) {
	cases := []struct {
		name  string
		title string
		year  int
		want  string
	}{
		{"普通", "某番", 2020, "某番|2020"},
		{"大小写与首尾空白归一", "  某番 ReDive  ", 2020, "某番 redive|2020"},
		{"年份未知写 0", "某番", 0, "某番|0"},
		{"空标题不给身份", "", 2020, ""},
		{"只有空白不给身份", "   ", 2020, ""},
	}
	for _, c := range cases {
		if got := scanKey(c.title, c.year); got != c.want {
			t.Errorf("%s: scanKey(%q, %d) = %q，期望 %q", c.name, c.title, c.year, got, c.want)
		}
	}
}

// TestNormalizeTitle 是这次修复的核心：不同来源对同一部作品的写法差异
// （半角/全角冒号、空白、斜杠、词序）必须归一化到同一个键上。
func TestNormalizeTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"半角冒号", "公主连结！Re:Dive", "公主连结redive"},
		{"全角冒号", "公主连结！Re：Dive", "公主连结redive"},
		{"没有冒号", "公主连结！ReDive", "公主连结redive"},
		{"最全的写法", "公主连结！Re：Dive（2020）", "公主连结redive2020"},
		{"空白", "某番  完全版", "某番完全版"},
		{"斜杠与中点", "心灵盟友／BUDDY COMPLEX", "心灵盟友buddycomplex"},
		{"波浪线", "ONE～辉之季节～", "one辉之季节"},
		{"词序不同的那一半", "辉之季节／ONE", "辉之季节one"},
		{"大写小写", "Fate／Strange Fake", "fatestrangefake"},
		{"中日文不动", "战姬绝唱Symphogear", "战姬绝唱symphogear"},
		{"空", "", ""},
	}
	for _, c := range cases {
		if got := normalizeTitle(c.in); got != c.want {
			t.Errorf("%s: normalizeTitle(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// TestTitleSimilar 守住「像同一部」的判定与它的边界：
// 认领只在唯一候选时生效，所以这里既要认得出改写的写法，也不能把续作 / 外传认进来。
func TestTitleSimilar(t *testing.T) {
	same := [][2]string{
		{"公主连结redive", "公主连结redive"},
		{"战姬绝唱", "战姬绝唱symphogear"},           // nfo 加了英文副标题
		{"buddycomplex", "心灵盟友buddycomplex"}, // nfo 加了中文前缀
		{"one辉之季节", "辉之季节one"},               // 词序不同
		{"rdg濒危物种少女", "濒危物种少女"},              // nfo 多了缩写前缀
	}
	for _, p := range same {
		if !titleSimilar(p[0], p[1]) || !titleSimilar(p[1], p[0]) {
			t.Errorf("%q 与 %q 应当判为相似", p[0], p[1])
		}
	}
	// 硬底线：空串绝不相似（否则空标题会命中一大片候选）。
	if titleSimilar("", "某番") || titleSimilar("某番", "") || titleSimilar("", "") {
		t.Error("空标题不该判为相似")
	}
	// 说明：titleSimilar 本身是**宽松**的（包含关系就算「像」）—— 决定要不要认领的是
	// pickClaim：候选必须唯一、年份相容，而且候选**还没有自己的身份**（见 claimCandidate）。
	// 所以「龙珠 / 龙珠Z」这类同名前缀的续作会被「不唯一」或「已有身份」挡住。
}

// TestPickClaim 守住认领的三个条件：候选唯一、年份相容、不抢已有身份的条目。
func TestPickClaim(t *testing.T) {
	base := []identityCandidate{
		{ID: 1, Title: "某番 完全版", Year: i32(2020)},
	}

	if c, ok := pickClaim(base, "某番", 2020); !ok || c.ID != 1 {
		t.Fatalf("唯一候选 + 同年份应当认领，实际 ok=%v id=%d", ok, c.ID)
	}
	// 年份被 nfo 改过（差 1~3 年）也要认
	if c, ok := pickClaim(base, "某番", 2018); !ok || c.ID != 1 {
		t.Fatalf("年份差 2 年应当认领（nfo 改过），实际 ok=%v", ok)
	}
	// 相差太远 → 说明是另一部作品
	if _, ok := pickClaim(base, "某番", 1998); ok {
		t.Fatal("年份差 22 年不该认领")
	}
	// 任一方没有年份 → 不拿年份否决
	if c, ok := pickClaim([]identityCandidate{{ID: 2, Title: "某番", Year: nil}}, "某番", 2020); !ok || c.ID != 2 {
		t.Fatalf("候选没有年份时应当认领，实际 ok=%v", ok)
	}
	// 多个候选 → 不认（宁可漏认也不错认）
	multi := []identityCandidate{
		{ID: 3, Title: "某番", Year: i32(2020)},
		{ID: 4, Title: "某番 完全版", Year: i32(2020)},
	}
	if _, ok := pickClaim(multi, "某番", 2020); ok {
		t.Fatal("多个候选时不该认领")
	}
	// 完全不搭的标题
	if _, ok := pickClaim([]identityCandidate{{ID: 5, Title: "另一部", Year: i32(2021)}}, "某番", 2020); ok {
		t.Fatal("标题不像的不该认领")
	}
	// 空标题不认领
	if _, ok := pickClaim(base, "", 2020); ok {
		t.Fatal("空标题不该认领")
	}
}
