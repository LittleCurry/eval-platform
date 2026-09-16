package eval

import (
	"math"
	"strings"
	"testing"
)

// ---- Wilcoxon ----

func TestWilcoxonHandComputedSmallSample(t *testing.T) {
	// 手算样例: 差值 [1, 2, 3, 4, 5], 无并列, 全为正 -> W+ = 1+2+3+4+5 = 15
	// 精确分布: 2^5 = 32 种符号组合, |W+ - 7.5| >= 7.5 的组合只有全正与全负 2 种 -> p = 2/32 = 0.0625
	stat, p, ok := WilcoxonSignedRank([]float64{1, 2, 3, 4, 5})
	if !ok {
		t.Fatal("五个非零差值应当可检验")
	}
	if stat != 15 {
		t.Fatalf("W+ 应为 15, 实际 %v", stat)
	}
	if math.Abs(p-0.0625) > 1e-9 {
		t.Fatalf("双侧精确 p 应为 0.0625, 实际 %v", p)
	}
}

func TestWilcoxonZerosAreDropped(t *testing.T) {
	// 差值为 0 的样本按惯例剔除: [1, -1, 2, 3, 4, 5] 里的 0 不影响结果
	withZero, _, _ := WilcoxonSignedRank([]float64{1, 2, 3, 4, 5, 0})
	withoutZero, _, _ := WilcoxonSignedRank([]float64{1, 2, 3, 4, 5})
	if withZero != withoutZero {
		t.Fatalf("0 差值应被剔除: %v vs %v", withZero, withoutZero)
	}
}

func TestWilcoxonAllZeroIsNotTestable(t *testing.T) {
	_, p, ok := WilcoxonSignedRank([]float64{0, 0, 0})
	if ok {
		t.Fatal("全为 0 时无法检验, ok 应为 false")
	}
	if p != 1 {
		t.Fatalf("不可检验时 p 记 1, 实际 %v", p)
	}
}

func TestWilcoxonSymmetricDiffsGivesPOne(t *testing.T) {
	// 完全对称的正负差 -> 毫无证据, p 应接近 1
	_, p, ok := WilcoxonSignedRank([]float64{1, -1, 2, -2, 3, -3, 4, -4})
	if !ok {
		t.Fatal("应当可检验")
	}
	if p < 0.9 {
		t.Fatalf("对称差值不该显著, p=%v", p)
	}
}

func TestWilcoxonTiesUseAverageRanks(t *testing.T) {
	// 绝对值并列(两个 2): 它们占第 1、2 位, 平均秩 = (1+2)/2 = 1.5; 5 拿第 3 位;
	// 所以 [2, -2, 5] 的 W+ = 1.5(来自 +2) + 3(来自 +5) = 4.5
	stat, _, ok := WilcoxonSignedRank([]float64{2, -2, 5})
	if !ok {
		t.Fatal("应当可检验")
	}
	if stat != 4.5 {
		t.Fatalf("并列应取平均秩: W+ 应为 4.5, 实际 %v", stat)
	}
}

func TestWilcoxonLargeSampleUsesNormalApproximation(t *testing.T) {
	// n=30 全为正 -> 正态近似下 p 极小(<1e-5), 且沿两个实现路径都不该 panic
	diffs := make([]float64, 30)
	for index := range diffs {
		diffs[index] = float64(index%5 + 1)
	}
	stat, p, ok := WilcoxonSignedRank(diffs)
	if !ok || stat <= 0 {
		t.Fatalf("大样本应可检验: stat=%v ok=%v", stat, ok)
	}
	if p >= 1e-5 {
		t.Fatalf("30 个全正差值应当极显著, p=%v", p)
	}
}

// ---- ComputeAB ----

func abCase(qid, category, difficulty string, recall float64, flags ...string) ABCase {
	return ABCase{
		QID:        qid,
		Category:   category,
		Difficulty: difficulty,
		Metrics:    map[string]float64{"recall": recall, "reciprocal_rank": recall, "hit": recall, "precision": recall},
		Flags:      flags,
	}
}

func TestComputeABCountsFixedAndBroke(t *testing.T) {
	left := []ABCase{
		abCase("q1", "线索", "易", 0, "retrieval_miss"),      // 修好了
		abCase("q2", "线索", "易", 1),                        // 一直干净
		abCase("q3", "客户", "中", 1),                        // 变坏了
		abCase("q4", "客户", "中", 0.5, "retrieval_partial"), // 标签变化: 既不算好也不算坏
	}
	right := []ABCase{
		abCase("q1", "线索", "易", 1),
		abCase("q2", "线索", "易", 1),
		abCase("q3", "客户", "中", 0, "hallucination"),
		abCase("q4", "客户", "中", 0.5, "retrieval_low_rank"),
	}

	report := ComputeAB(left, right, ABOptions{})

	if !report.Comparable {
		t.Fatalf("同题集应当可比: %s", report.Reason)
	}
	if len(report.Fixed) != 1 || report.Fixed[0].QID != "q1" {
		t.Fatalf("q1 应判为修好: %+v", report.Fixed)
	}
	if len(report.Broke) != 1 || report.Broke[0].QID != "q3" {
		t.Fatalf("q3 应判为变坏: %+v", report.Broke)
	}
	if len(report.Changed) != 1 || report.Changed[0].QID != "q4" {
		t.Fatalf("q4 标签变了但不好不坏, 应只进 changed: %+v", report.Changed)
	}
	if report.Fixed[0].FlagsLeft[0] != "retrieval_miss" || len(report.Fixed[0].FlagsRight) != 0 {
		t.Fatalf("修好题要带上两侧标签证据: %+v", report.Fixed[0])
	}
}

func TestComputeABSummaryAndNoiseFloor(t *testing.T) {
	// q4 的差值是 0.001 < 噪声底 2.1e-3 -> 不宣称变化
	left := []ABCase{abCase("q1", "线索", "易", 0.5), abCase("q2", "线索", "易", 0.5)}
	right := []ABCase{abCase("q1", "线索", "易", 0.5009), abCase("q2", "线索", "易", 0.5)}

	report := ComputeAB(left, right, ABOptions{})
	delta := report.Summary["recall"]

	if !delta.BelowNoise {
		t.Fatalf("差值 %v 落在噪声底内, 应标 below_noise", delta.Delta)
	}
	if delta.Significant {
		t.Fatal("落在噪声底内不许说显著")
	}
	if delta.Improved != 0 || delta.Worsened != 0 || delta.Unchanged != 2 {
		t.Fatalf("噪声底内的抖动应计入 unchanged: %+v", delta)
	}
}

func TestComputeABSignificantImprovement(t *testing.T) {
	left := make([]ABCase, 20)
	right := make([]ABCase, 20)
	for index := 0; index < 20; index++ {
		qid := "q" + string(rune('a'+index))
		left[index] = abCase(qid, "线索", "易", 0)
		right[index] = abCase(qid, "线索", "易", 1)
	}

	report := ComputeAB(left, right, ABOptions{})
	delta := report.Summary["recall"]

	if delta.Delta != 1 || delta.Improved != 20 {
		t.Fatalf("20 题全从 0 到 1: %+v", delta)
	}
	if !delta.Significant {
		t.Fatalf("这么大的差应当显著: p=%v", delta.PValue)
	}
}

func TestComputeABStratifiesByFlag(t *testing.T) {
	left := []ABCase{
		abCase("q1", "线索", "易", 0, "retrieval_miss"),
		abCase("q2", "线索", "易", 0, "retrieval_miss"),
		abCase("q3", "客户", "中", 1, "retrieval_partial"),
	}
	right := []ABCase{
		abCase("q1", "线索", "易", 1),
		abCase("q2", "线索", "易", 1),
		abCase("q3", "客户", "中", 1, "retrieval_low_rank"),
	}

	report := ComputeAB(left, right, ABOptions{})

	byKey := map[string]Stratum{}
	for _, stratum := range report.ByFlag {
		byKey[stratum.Key] = stratum
	}
	miss, ok := byKey["retrieval_miss"]
	if !ok {
		t.Fatalf("应统计 retrieval_miss 分组: %+v", report.ByFlag)
	}
	if miss.FlaggedLeft != 2 || miss.FlaggedRight != 0 {
		t.Fatalf("retrieval_miss 应 2 -> 0: %+v", miss)
	}
	if miss.Improved != 2 {
		t.Fatalf("这两题应记 2 个改善: %+v", miss)
	}
	partial := byKey["retrieval_partial"]
	if partial.FlaggedLeft != 1 || partial.FlaggedRight != 0 {
		t.Fatalf("retrieval_partial 应 1 -> 0: %+v", partial)
	}
	lowRank := byKey["retrieval_low_rank"]
	if lowRank.FlaggedLeft != 0 || lowRank.FlaggedRight != 1 {
		t.Fatalf("retrieval_low_rank 应 0 -> 1: %+v", lowRank)
	}
}

func TestComputeABMarksIncomparableWhenSharedTooFew(t *testing.T) {
	left := []ABCase{abCase("q1", "线索", "易", 1), abCase("q2", "线索", "易", 1), abCase("q3", "线索", "易", 1)}
	right := []ABCase{abCase("q1", "线索", "易", 1), abCase("q9", "线索", "易", 1)}

	report := ComputeAB(left, right, ABOptions{})

	if report.Comparable {
		t.Fatal("共同题目只有 1/3, 应判不可比")
	}
	if report.Reason == "" {
		t.Fatal("不可比必须给原因")
	}
}

func TestComputeABEmptyRunsDoNotPanic(t *testing.T) {
	report := ComputeAB(nil, nil, ABOptions{})
	if report.Comparable {
		t.Fatal("空 run 不可比")
	}
	if len(report.Summary) != 0 {
		t.Fatalf("空输入不该产出指标: %+v", report.Summary)
	}
}

func TestComputeABIdenticalRunsShowNoDifference(t *testing.T) {
	// 负控: 同配置两次 run(如演练对 #100/#101)必须被判成"没有任何差异"
	cases := []ABCase{
		abCase("q1", "线索", "易", 1),
		abCase("q2", "线索", "易", 0.5),
		abCase("q3", "客户", "中", 0, "retrieval_miss"),
	}

	report := ComputeAB(cases, cases, ABOptions{})
	delta := report.Summary["recall"]

	if delta.Delta != 0 || delta.Improved != 0 || delta.Worsened != 0 {
		t.Fatalf("同配置不该有差异: %+v", delta)
	}
	if delta.Significant {
		t.Fatal("没有任何差值却说显著, 就是在制造噪声")
	}
	if len(report.Fixed)+len(report.Broke)+len(report.Changed) != 0 {
		t.Fatalf("不该有翻转题: %+v %+v %+v", report.Fixed, report.Broke, report.Changed)
	}
}

func TestComputeABWarnsWhenOnlyOneSideHasAttribution(t *testing.T) {
	// 场景: A 做过归因而 B 没做过 -> B 的 flags 全是空的, 每道有标签的题都会变成"被修好"。
	// 这种假阳性必须被显式喊出来, 而不是当成真实结论。
	left := []ABCase{abCase("q1", "线索", "易", 1, "retrieval_partial"), abCase("q2", "线索", "易", 1)}
	right := []ABCase{abCase("q1", "线索", "易", 1), abCase("q2", "线索", "易", 1)}

	report := ComputeAB(left, right, ABOptions{LeftAttribution: true, RightAttribution: false})

	if len(report.Fixed) != 1 {
		t.Fatalf("数据本身确实会算出 1 道 fixed: %+v", report.Fixed)
	}
	if !strings.Contains(report.AttributionMissing, "右侧 run 未做过归因") {
		t.Fatalf("必须提示右侧没做过归因: %q", report.AttributionMissing)
	}
	if !strings.Contains(report.AttributionMissing, "make attribution") {
		t.Fatalf("提示要给可执行的下一步: %q", report.AttributionMissing)
	}
}

func TestComputeABNoWarningWhenBothSidesReady(t *testing.T) {
	left := []ABCase{abCase("q1", "线索", "易", 1, "retrieval_miss")}
	right := []ABCase{abCase("q1", "线索", "易", 1)}

	report := ComputeAB(left, right, ABOptions{LeftAttribution: true, RightAttribution: true})

	if report.AttributionMissing != "" {
		t.Fatalf("两侧都做过归因时不该警告: %q", report.AttributionMissing)
	}
}

func TestComputeABNoWarningWhenNeitherSideHasAttribution(t *testing.T) {
	// 两侧都没有归因 -> flags 一律为空, Fixed/Broke 自然为空, 不会产生假阳性, 不必打扰
	left := []ABCase{abCase("q1", "线索", "易", 1)}
	right := []ABCase{abCase("q1", "线索", "易", 1)}

	report := ComputeAB(left, right, ABOptions{})

	if report.AttributionMissing != "" {
		t.Fatalf("两侧都没做过时不该警告: %q", report.AttributionMissing)
	}
}

// ---- 生成侧指标(M5-1 尾巴) ----

func abJudged(qid string, recall float64, claims, unsupported int, rubric *float64) ABCase {
	item := abCase(qid, "线索", "易", recall)
	item.Judge = &ABJudge{Claims: claims, Supported: claims - unsupported, Unsupported: unsupported}
	if rubric != nil {
		item.Judge.HasRubric = true
		item.Judge.Helpfulness = *rubric
		item.Judge.Relevance = *rubric
	}
	return item
}

func TestComputeABGenerationMetricsUseOnlyJudgedPairs(t *testing.T) {
	// q1/q2 两侧都有判定(可配对); q3 只有左侧有判定 -> 必须被排除,
	// 否则"右侧没判定"会被读成"右侧零幻觉"(M4-2 的老教训)
	score := 4.0
	left := []ABCase{
		abJudged("q1", 1, 4, 0, &score),
		abJudged("q2", 1, 4, 2, &score),
		abJudged("q3", 1, 4, 4, &score),
	}
	right := []ABCase{
		abJudged("q1", 1, 4, 0, &score),
		abJudged("q2", 1, 4, 0, &score),
		abCase("q3", "线索", "易", 1), // 无判定
	}

	report := ComputeAB(left, right, ABOptions{IncludeGeneration: true})

	if report.JudgedCases != 2 {
		t.Fatalf("q3 只有左侧有判定 -> 两侧都有判定的题数是 2, 实际 %d", report.JudgedCases)
	}
	hallucination := report.Summary["hallucination_rate"]
	if hallucination.Cases != 2 {
		t.Fatalf("只有 2 题两侧都有断言, 实际 %d", hallucination.Cases)
	}
	// 左: (0/4 + 2/4)/2 = 0.25; 右: 0 —— 必须只算这两题, 不能把 q3 的 4/4 混进来
	if hallucination.Left != 0.25 || hallucination.Right != 0 {
		t.Fatalf("幻觉率均值算错: %+v", hallucination)
	}
	if hallucination.Improved != 1 || hallucination.Worsened != 0 {
		t.Fatalf("q2 从 0.5 降到 0 应记 1 个改善: %+v", hallucination)
	}
}

func TestComputeABGenerationMetricsSkipCasesWithoutClaims(t *testing.T) {
	// 答案只说"资料中未提及"-> claims=0 -> 三个率无意义, 不进样本; 但 rubric 仍可比
	score := 4.0
	left := []ABCase{abJudged("q1", 1, 0, 0, &score), abJudged("q2", 1, 4, 1, &score)}
	right := []ABCase{abJudged("q1", 1, 0, 0, &score), abJudged("q2", 1, 4, 0, &score)}

	report := ComputeAB(left, right, ABOptions{IncludeGeneration: true})

	if report.Summary["hallucination_rate"].Cases != 1 {
		t.Fatalf("claims=0 的题不该进率值样本: %+v", report.Summary["hallucination_rate"])
	}
	if report.Summary["helpfulness"].Cases != 2 {
		t.Fatalf("有 rubric 的题都该进分数样本: %+v", report.Summary["helpfulness"])
	}
}

func TestComputeABGenerationMetricsNeedRubricOnBothSides(t *testing.T) {
	// 一侧没打 rubric -> 分数指标不可比(不能把"没打分"当成 0 分)
	score := 5.0
	left := []ABCase{abJudged("q1", 1, 4, 0, &score)}
	right := []ABCase{abJudged("q1", 1, 4, 0, nil)}

	report := ComputeAB(left, right, ABOptions{IncludeGeneration: true})

	if report.Summary["helpfulness"].Cases != 0 {
		t.Fatalf("两侧都缺 rubric 时不该有样本: %+v", report.Summary["helpfulness"])
	}
	if report.Summary["hallucination_rate"].Cases != 1 {
		t.Fatalf("率值仍可比(两侧都有断言): %+v", report.Summary["hallucination_rate"])
	}
}

func TestComputeABGenerationNoteWhenNoJudgeAtAll(t *testing.T) {
	// 只跑检索的两次 run: 生成侧没有任何可配对的判定 -> 必须说明"为什么没得比",
	// 而不是给出一排 0 让人误以为"两次都没有幻觉"
	left := []ABCase{abCase("q1", "线索", "易", 1), abCase("q2", "线索", "易", 0)}
	right := []ABCase{abCase("q1", "线索", "易", 1), abCase("q2", "线索", "易", 1)}

	report := ComputeAB(left, right, ABOptions{IncludeGeneration: true})

	if report.JudgedCases != 0 {
		t.Fatalf("没有判定时配对样本应为 0: %d", report.JudgedCases)
	}
	if !strings.Contains(report.GenerationNote, "没有判定") {
		t.Fatalf("要说明原因: %q", report.GenerationNote)
	}
	if report.Summary["hallucination_rate"].Cases != 0 {
		t.Fatalf("没有判定时不该有样本量: %+v", report.Summary["hallucination_rate"])
	}
}

func TestComputeABRetrievalMetricsReportCaseCount(t *testing.T) {
	// 检索侧指标的样本量 = 共同题目数(前端要把这个数显示出来)
	left := []ABCase{abCase("q1", "线索", "易", 1), abCase("q2", "线索", "易", 0)}
	right := []ABCase{abCase("q1", "线索", "易", 1), abCase("q2", "线索", "易", 1)}

	report := ComputeAB(left, right, ABOptions{})

	if report.Summary["recall"].Cases != 2 {
		t.Fatalf("检索侧样本量应为共同题目数: %+v", report.Summary["recall"])
	}
	if report.GenerationNote != "" {
		t.Fatalf("没要求生成侧对比时不该有生成侧说明: %q", report.GenerationNote)
	}
}

func TestComputeABRespectsMetricDirection(t *testing.T) {
	// 幻觉率是"越低越好": 下降必须记改善(而不是恶化), 上升必须记恶化
	score := 4.0
	left := []ABCase{abJudged("q1", 1, 4, 3, &score), abJudged("q2", 1, 4, 0, &score)}
	right := []ABCase{abJudged("q1", 1, 4, 0, &score), abJudged("q2", 1, 4, 2, &score)}

	report := ComputeAB(left, right, ABOptions{IncludeGeneration: true})
	hallucination := report.Summary["hallucination_rate"]

	if hallucination.Improved != 1 || hallucination.Worsened != 1 {
		t.Fatalf("一降一升: 应各记一次改善与恶化, 实际 %+v", hallucination)
	}
	if hallucination.HigherIsBetter {
		t.Fatal("幻觉率必须标成越低越好, 否则前端会把下降涂成红色")
	}
	if report.Summary["claim_support_rate"].HigherIsBetter != true {
		t.Fatal("断言支持率是越高越好")
	}
	if report.Summary["recall"].HigherIsBetter != true {
		t.Fatal("检索指标都是越高越好")
	}
}
