package eval

import (
	"math"
	"strings"
	"testing"
)

// kappaTolerance κ 值是浮点算出来的, 卡到 1e-9 再比较(报告里会四舍五入到 4 位)。
const kappaTolerance = 1e-9

func almostEqual(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > kappaTolerance {
		t.Fatalf("%s 应为 %.6f, 实际 %.6f", label, want, got)
	}
}

func intPtr(value int) *int {
	return &value
}

// TestCohenKappaTextbook 教科书例子: 100 题, 一致 80 题, 两侧边际各半。
// po = 0.8, pe = 0.5, κ = (0.8-0.5)/0.5 = 0.6 —— 手工算得出来的值, 用来卡公式实现。
func TestCohenKappaTextbook(t *testing.T) {
	labelsA := make([]string, 0, 100)
	labelsB := make([]string, 0, 100)
	appendPairs := func(labelA, labelB string, count int) {
		for index := 0; index < count; index++ {
			labelsA = append(labelsA, labelA)
			labelsB = append(labelsB, labelB)
		}
	}
	appendPairs("positive", "positive", 40)
	appendPairs("positive", "negative", 10)
	appendPairs("negative", "positive", 10)
	appendPairs("negative", "negative", 40)

	almostEqual(t, "κ", cohenKappa(labelsA, labelsB), 0.6)
}

// TestCohenKappaPerfectAgreement 完全一致且两侧都有变异 → κ = 1。
func TestCohenKappaPerfectAgreement(t *testing.T) {
	labelsA := []string{"positive", "negative", "positive", "negative"}
	labelsB := []string{"positive", "negative", "positive", "negative"}
	almostEqual(t, "κ", cohenKappa(labelsA, labelsB), 1)
}

// TestCohenKappaUndefinedWhenNoVariance 一侧只用了一个类别: pe = 1, κ 无定义 → 返回 0。
// 这是刻意的: 返回 1 会让人误以为"完美一致", 返回 0 才是"这个指标在这里没有信息量"。
func TestCohenKappaUndefinedWhenNoVariance(t *testing.T) {
	if got := cohenKappa([]string{"positive", "positive"}, []string{"positive", "positive"}); got != 0 {
		t.Fatalf("pe=1 时 κ 应为 0(无定义), 实际 %.6f", got)
	}
	if got := cohenKappa(nil, nil); got != 0 {
		t.Fatalf("空输入应为 0, 实际 %.6f", got)
	}
	if got := cohenKappa([]string{"positive"}, []string{"positive", "negative"}); got != 0 {
		t.Fatalf("长度不一致应为 0, 实际 %.6f", got)
	}
}

// TestCohenKappaPunishesAlwaysNegative 区分"高一致率"与"有信息量"。
// 10 题里人工只说 1 次有幻觉, judge 一律说"没有幻觉": 一致率 90%, 但 κ = 0
// —— 因为它没有比常数分类器多任何信息。这正是报告要算 κ 而不是只报一致率的原因。
func TestCohenKappaPunishesAlwaysNegative(t *testing.T) {
	labelsA := []string{"positive", "negative", "negative", "negative", "negative",
		"negative", "negative", "negative", "negative", "negative"}
	labelsB := []string{"negative", "negative", "negative", "negative", "negative",
		"negative", "negative", "negative", "negative", "negative"}

	observed := 0
	for index := range labelsA {
		if labelsA[index] == labelsB[index] {
			observed++
		}
	}
	if observed != 9 {
		t.Fatalf("一致数应为 9, 实际 %d", observed)
	}
	if got := cohenKappa(labelsA, labelsB); got != 0 {
		t.Fatalf("常数分类器的 κ 应为 0, 实际 %.6f", got)
	}
}

// TestComputeBinaryConfusion 逐格核对混淆矩阵, 并验证两类"不该进样本"的题被排除:
// 人工说"看不清"的, 以及 judge 没有判定结论的(claims 为空不是"judge 说没有幻觉")。
func TestComputeBinaryConfusion(t *testing.T) {
	items := []CalibrationItem{
		{CaseID: 1, HumanVerdict: VerdictHallucinated, JudgeHasOpinion: true, JudgeUnsupported: 2},  // TP
		{CaseID: 2, HumanVerdict: VerdictHallucinated, JudgeHasOpinion: true, JudgeUnsupported: 0},  // FN
		{CaseID: 3, HumanVerdict: VerdictFaithful, JudgeHasOpinion: true, JudgeUnsupported: 1},      // FP
		{CaseID: 4, HumanVerdict: VerdictFaithful, JudgeHasOpinion: true, JudgeUnsupported: 0},      // TN
		{CaseID: 5, HumanVerdict: VerdictUnclear, JudgeHasOpinion: true, JudgeUnsupported: 3},       // 排除
		{CaseID: 6, HumanVerdict: VerdictFaithful, JudgeHasOpinion: false, JudgeUnsupported: 0},     // 排除
		{CaseID: 7, HumanVerdict: VerdictHallucinated, JudgeHasOpinion: false, JudgeUnsupported: 9}, // 排除
	}

	got := computeBinary(items)
	if got == nil {
		t.Fatal("不应返回 nil")
	}
	if got.Pairs != 4 {
		t.Fatalf("样本数应为 4, 实际 %d", got.Pairs)
	}
	if got.TruePositive != 1 || got.FalsePositive != 1 || got.TrueNegative != 1 || got.FalseNegative != 1 {
		t.Fatalf("混淆矩阵应为各 1, 实际 TP=%d FP=%d TN=%d FN=%d",
			got.TruePositive, got.FalsePositive, got.TrueNegative, got.FalseNegative)
	}
	if got.JudgePositive != 2 || got.HumanPositive != 2 {
		t.Fatalf("正例数应为 2/2, 实际 judge=%d human=%d", got.JudgePositive, got.HumanPositive)
	}
	if got.Agreement != 0.5 {
		t.Fatalf("一致率应为 0.5, 实际 %.4f", got.Agreement)
	}
	// 完全对称的二分类: po = 0.5, pe = 0.5 → κ = 0
	if got.Kappa != 0 {
		t.Fatalf("κ 应为 0, 实际 %.4f", got.Kappa)
	}
	if got.ExcludedUnclear != 1 {
		t.Fatalf("应记录 1 题因\"看不清\"被排除, 实际 %d", got.ExcludedUnclear)
	}
}

// TestComputeBinaryNoPairs 一题有效样本都没有 → nil(而不是一份全 0 的假报告)。
func TestComputeBinaryNoPairs(t *testing.T) {
	items := []CalibrationItem{
		{CaseID: 1, HumanVerdict: VerdictUnclear, JudgeHasOpinion: true, JudgeUnsupported: 1},
		{CaseID: 2, HumanVerdict: VerdictFaithful, JudgeHasOpinion: false},
	}
	if got := computeBinary(items); got != nil {
		t.Fatalf("无有效样本应返回 nil, 实际 %+v", got)
	}
}

// TestComputeScoresMetrics 手工核对的分数校准: 3 对有效分数(1 题 judge 没打分, 不算 0 分)。
func TestComputeScoresMetrics(t *testing.T) {
	items := []CalibrationItem{
		{CaseID: 1, HumanHelpfulness: intPtr(5), JudgeHelpfulness: intPtr(4)},
		{CaseID: 2, HumanHelpfulness: intPtr(3), JudgeHelpfulness: intPtr(3)},
		{CaseID: 3, HumanHelpfulness: intPtr(2), JudgeHelpfulness: intPtr(5)},
		{CaseID: 4, HumanHelpfulness: intPtr(4), JudgeHelpfulness: nil},
	}

	got := computeScores(items, scoreHelpfulness)
	if got == nil {
		t.Fatal("不应返回 nil")
	}
	if got.Pairs != 3 {
		t.Fatalf("样本数应为 3, 实际 %d", got.Pairs)
	}
	if got.ExactAgreement != 0.3333 {
		t.Fatalf("完全一致率应为 1/3≈0.3333, 实际 %.4f", got.ExactAgreement)
	}
	if got.Within1 != 0.6667 {
		t.Fatalf("±1 一致率应为 2/3≈0.6667, 实际 %.4f", got.Within1)
	}
	if got.MAE != 1.3333 {
		t.Fatalf("MAE 应为 4/3≈1.3333, 实际 %.4f", got.MAE)
	}
	if got.HumanMean != 3.3333 || got.JudgeMean != 4 {
		t.Fatalf("均值应为 3.3333/4, 实际 %.4f/%.4f", got.HumanMean, got.JudgeMean)
	}
	// judge 整体比人工高 0.67 分 → 正数表示 judge 更宽松
	if got.Bias != 0.6667 {
		t.Fatalf("偏差应为 +0.6667, 实际 %.4f", got.Bias)
	}
	if got.Confusion[4][3] != 1 || got.Confusion[2][2] != 1 || got.Confusion[1][4] != 1 {
		t.Fatalf("5×5 混淆矩阵落点不对: %v", got.Confusion)
	}
	if got.Confusion[3][4] != 0 {
		t.Fatalf("未出现的组合应保持 0, 实际 %d", got.Confusion[3][4])
	}
}

// TestComputeScoresOutOfRangeIgnored 越界分数不应被静默算进均值(9 分会被当成离群值污染 MAE)。
func TestComputeScoresOutOfRangeIgnored(t *testing.T) {
	items := []CalibrationItem{
		{CaseID: 1, HumanHelpfulness: intPtr(9), JudgeHelpfulness: intPtr(3)},
		{CaseID: 2, HumanHelpfulness: intPtr(3), JudgeHelpfulness: intPtr(0)},
	}
	if got := computeScores(items, scoreHelpfulness); got != nil {
		t.Fatalf("全部越界应返回 nil, 实际 %+v", got)
	}
}

// TestComputeInterAnnotatorOnlyOverlap 人工之间的一致性只算两人都打过分的那部分题;
// 并且重合题不足 2 时不给 κ(1 题算出来的"一致性"是噪声)。
func TestComputeInterAnnotatorOnlyOverlap(t *testing.T) {
	itemsA := []CalibrationItem{
		{CaseID: 1, HumanVerdict: VerdictHallucinated, HumanHelpfulness: intPtr(4)},
		{CaseID: 2, HumanVerdict: VerdictFaithful, HumanHelpfulness: intPtr(3)},
		{CaseID: 3, HumanVerdict: VerdictFaithful, HumanHelpfulness: intPtr(5)},
	}
	itemsB := []CalibrationItem{
		{CaseID: 1, HumanVerdict: VerdictHallucinated, HumanHelpfulness: intPtr(4)},
		{CaseID: 2, HumanVerdict: VerdictHallucinated, HumanHelpfulness: intPtr(3)},
		{CaseID: 4, HumanVerdict: VerdictFaithful, HumanHelpfulness: intPtr(2)},
	}

	got := computeInterAnnotator("anna", "bob", itemsA, itemsB)
	if got == nil {
		t.Fatal("不应返回 nil")
	}
	if got.BinaryPairs != 2 || got.ScorePairs != 2 {
		t.Fatalf("重合题数应为 2/2, 实际 %d/%d", got.BinaryPairs, got.ScorePairs)
	}
	// 题 1、2 的 verdict: anna=[阳性, 阴性], bob=[阳性, 阳性] → po=0.5, pe=0.5 → κ=0
	if got.BinaryKappa == nil || *got.BinaryKappa != 0 {
		t.Fatalf("κ 应为 0, 实际 %v", got.BinaryKappa)
	}
	// helpfulness 两题都给的一样的分 → MAE 0, 完全一致率 100%
	if got.ScoreMAE == nil || *got.ScoreMAE != 0 {
		t.Fatalf("MAE 应为 0, 实际 %v", got.ScoreMAE)
	}
	if got.ScoreExactRate == nil || *got.ScoreExactRate != 1 {
		t.Fatalf("完全一致率应为 1, 实际 %v", got.ScoreExactRate)
	}

	single := computeInterAnnotator("anna", "bob", itemsA[:1], itemsB[:1])
	if single == nil {
		t.Fatal("不应返回 nil")
	}
	if single.BinaryKappa != nil {
		t.Fatalf("只重合 1 题时不该给 κ, 实际 %v", *single.BinaryKappa)
	}
}

// TestComputeCalibrationPrimaryAnnotator 两位标注员时, 主标注员取打分更多的那位;
// 题数相同则按名字序取第一个(结果必须稳定, 不能随 map 遍历顺序变)。
func TestComputeCalibrationPrimaryAnnotator(t *testing.T) {
	items := []CalibrationItem{
		{CaseID: 1, Annotator: "bob", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true,
			HumanHelpfulness: intPtr(4), JudgeHelpfulness: intPtr(4)},
		{CaseID: 2, Annotator: "bob", HumanVerdict: VerdictHallucinated, JudgeHasOpinion: true,
			JudgeUnsupported: 1, HumanHelpfulness: intPtr(3), JudgeHelpfulness: intPtr(3)},
		{CaseID: 3, Annotator: "bob", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true,
			HumanHelpfulness: intPtr(5), JudgeHelpfulness: intPtr(5)},
		{CaseID: 1, Annotator: "anna", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true,
			HumanHelpfulness: intPtr(4), JudgeHelpfulness: intPtr(4)},
		{CaseID: 2, Annotator: "anna", HumanVerdict: VerdictHallucinated, JudgeHasOpinion: true,
			HumanHelpfulness: intPtr(3), JudgeHelpfulness: intPtr(3)},
	}

	got := ComputeCalibration(155, items, CalibrationOptions{TotalCases: 60})
	if got.PrimaryAnnotator != "bob" {
		t.Fatalf("主标注员应为打分数更多的 bob, 实际 %s", got.PrimaryAnnotator)
	}
	if got.CasesWithGold != 3 {
		t.Fatalf("有金标的题数应为 3, 实际 %d", got.CasesWithGold)
	}
	if got.Coverage != 0.05 {
		t.Fatalf("覆盖率应为 3/60=0.05, 实际 %.4f", got.Coverage)
	}
	if got.InterAnnotator == nil {
		t.Fatal("两人有 2 道共同打分的题, 应给出人工之间的一致性")
	}
	if got.InterAnnotator.AnnotatorsA != "anna" || got.InterAnnotator.AnnotatorsB != "bob" {
		t.Fatalf("顺序应稳定为 anna/bob, 实际 %s/%s",
			got.InterAnnotator.AnnotatorsA, got.InterAnnotator.AnnotatorsB)
	}
	if got.InterAnnotator.ScorePairs != 2 {
		t.Fatalf("重合计分题应为 2, 实际 %d", got.InterAnnotator.ScorePairs)
	}
	if got.InterAnnotator.BinaryKappa == nil {
		t.Fatal("两人对 2 道题的 verdict 完全一致, 应给出 κ")
	}
	almostEqual(t, "人工之间 κ", *got.InterAnnotator.BinaryKappa, 1)

	tie := ComputeCalibration(155, []CalibrationItem{
		{CaseID: 1, Annotator: "bob", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true},
		{CaseID: 2, Annotator: "anna", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true},
	}, CalibrationOptions{TotalCases: 60})
	if tie.PrimaryAnnotator != "anna" {
		t.Fatalf("并列时应取名字序最小的 anna, 实际 %s", tie.PrimaryAnnotator)
	}
}

// TestComputeCalibrationNotes 报告要主动暴露"这次结论不可靠"的原因, 而不是安静地给一个数。
func TestComputeCalibrationNotes(t *testing.T) {
	empty := ComputeCalibration(155, nil, CalibrationOptions{TotalCases: 60})
	if empty.Binary != nil || empty.Helpfulness != nil || empty.Relevance != nil {
		t.Fatalf("没有金标时不该有校准结果, 实际 %+v", empty)
	}
	if len(empty.Notes) == 0 {
		t.Fatal("没有金标时应给出提示")
	}

	items := []CalibrationItem{
		{CaseID: 1, Annotator: "anna", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true,
			HumanHelpfulness: intPtr(4), JudgeHelpfulness: intPtr(4),
			HumanRelevance: intPtr(5), JudgeRelevance: intPtr(5)},
		{CaseID: 2, Annotator: "anna", HumanVerdict: VerdictUnclear, JudgeHasOpinion: true,
			JudgeUnsupported: 1},
	}
	got := ComputeCalibration(155, items, CalibrationOptions{TotalCases: 60})
	if got.Binary == nil || got.Binary.Pairs != 1 {
		t.Fatalf("应有 1 对二分类样本, 实际 %+v", got.Binary)
	}
	if got.Helpfulness == nil || got.Helpfulness.Pairs != 1 || got.Helpfulness.MAE != 0 {
		t.Fatalf("helpfulness 应有 1 对且 MAE 为 0, 实际 %+v", got.Helpfulness)
	}
	if got.Relevance == nil || got.Relevance.Pairs != 1 {
		t.Fatalf("relevance 应有 1 对, 实际 %+v", got.Relevance)
	}

	joined := ""
	for _, note := range got.Notes {
		joined += note + "\n"
	}
	for _, want := range []string{"只有一位标注员", "少于 20 题", "覆盖率低于 50%", "看不清"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("提示里缺少 %q, 实际: %s", want, joined)
		}
	}
}

// TestComputeCalibrationReviewSignal 金标"有没有复核"必须出现在报告里:
// 没复核过的金标, κ 只是一个人 vs judge 的口径, 不是"人工"的普遍结论。
func TestComputeCalibrationReviewSignal(t *testing.T) {
	build := func(reviewedCount int) []CalibrationItem {
		items := make([]CalibrationItem, 0, 25)
		for index := 0; index < 25; index++ {
			items = append(items, CalibrationItem{
				CaseID: int64(index + 1), Annotator: "me",
				HumanVerdict: VerdictFaithful, JudgeHasOpinion: true,
				Reviewed: index < reviewedCount,
			})
		}
		return items
	}

	none := ComputeCalibration(155, build(0), CalibrationOptions{TotalCases: 25})
	if none.GoldReviewed != 0 {
		t.Fatalf("没有复核时计数应为 0: %d", none.GoldReviewed)
	}
	if !strings.Contains(strings.Join(none.Notes, "\n"), "都还没复核") {
		t.Fatalf("样本够大但零复核时要提醒: %v", none.Notes)
	}

	some := ComputeCalibration(155, build(8), CalibrationOptions{TotalCases: 25})
	if some.GoldReviewed != 8 {
		t.Fatalf("复核计数应为 8: %d", some.GoldReviewed)
	}
	if !strings.Contains(strings.Join(some.Notes, "\n"), "8/25") {
		t.Fatalf("要报出复核比例: %v", some.Notes)
	}

	// 样本很小时不提复核(那时更该先扩样本)
	small := ComputeCalibration(155, build(0)[:5], CalibrationOptions{TotalCases: 5})
	if strings.Contains(strings.Join(small.Notes, "\n"), "都还没复核") {
		t.Fatalf("5 题时不该先念叨复核: %v", small.Notes)
	}
}

// TestCollectDisagreements 判错清单: 漏判在前, "没意见"的题不算判错。
//
// 光有 κ 只能说"judge 行不行"; 要改 prompt 就得知道"它把哪几道题判错了"。
func TestCollectDisagreements(t *testing.T) {
	items := []CalibrationItem{
		// 误报(judge 说有幻觉, 人工说没有)
		{CaseID: 5, QID: "zjc-005", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true, JudgeUnsupported: 2},
		// 漏判(人工说有幻觉, judge 说没有) —— 更严重, 要排在前面
		{CaseID: 9, QID: "zjc-009", HumanVerdict: VerdictHallucinated, JudgeHasOpinion: true, JudgeUnsupported: 0},
		{CaseID: 3, QID: "zjc-003", HumanVerdict: VerdictHallucinated, JudgeHasOpinion: true, JudgeUnsupported: 0},
		// 一致的两题: 不算判错
		{CaseID: 1, QID: "zjc-001", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true, JudgeUnsupported: 0},
		{CaseID: 2, QID: "zjc-002", HumanVerdict: VerdictHallucinated, JudgeHasOpinion: true, JudgeUnsupported: 1},
		// "没意见"的题: 人工看不清 / judge 没结论
		{CaseID: 4, QID: "zjc-004", HumanVerdict: VerdictUnclear, JudgeHasOpinion: true, JudgeUnsupported: 3},
		{CaseID: 6, QID: "zjc-006", HumanVerdict: VerdictHallucinated, JudgeHasOpinion: false},
	}

	got := collectDisagreements(items)
	if len(got) != 3 {
		t.Fatalf("应有 3 条判错, 实际 %d: %+v", len(got), got)
	}
	if got[0].Kind != DisagreementMissed || got[1].Kind != DisagreementMissed {
		t.Fatalf("漏判要排在前面: %+v", got)
	}
	if got[0].CaseID != 3 || got[1].CaseID != 9 {
		t.Fatalf("同类内按 case_id 稳定排序: %+v", got)
	}
	if got[2].Kind != DisagreementFalseAlarm || got[2].CaseID != 5 {
		t.Fatalf("误报排最后: %+v", got)
	}
	if got[0].HumanVerdict != VerdictHallucinated || got[0].JudgeUnsupported != 0 {
		t.Fatalf("要带上人机两侧的判定依据: %+v", got[0])
	}
}

// TestCollectDisagreementsCap 清单最多 50 条: 再多就该去筛 bad case, 而不是在报告里翻页。
func TestCollectDisagreementsCap(t *testing.T) {
	items := make([]CalibrationItem, 0, 60)
	for index := 0; index < 60; index++ {
		items = append(items, CalibrationItem{
			CaseID: int64(index + 1), HumanVerdict: VerdictHallucinated,
			JudgeHasOpinion: true, JudgeUnsupported: 0,
		})
	}
	if got := collectDisagreements(items); len(got) != MaxDisagreements {
		t.Fatalf("应截断到 %d 条, 实际 %d", MaxDisagreements, len(got))
	}
}

// TestComputeCalibrationMissedNote 漏判要单独提示: 这类错会让幻觉流到线上。
func TestComputeCalibrationMissedNote(t *testing.T) {
	items := []CalibrationItem{
		{CaseID: 1, Annotator: "me", HumanVerdict: VerdictHallucinated, JudgeHasOpinion: true, JudgeUnsupported: 0},
		{CaseID: 2, Annotator: "me", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true, JudgeUnsupported: 1},
	}
	report := ComputeCalibration(155, items, CalibrationOptions{TotalCases: 2})
	if len(report.Disagreements) != 2 {
		t.Fatalf("两题都不一致: %+v", report.Disagreements)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "题是漏判") {
		t.Fatalf("漏判要单独提示: %v", report.Notes)
	}
}

// TestComputeCalibrationOnlyHelpfulness 只标了 helpfulness 时, relevance 不应凭空出现。
func TestComputeCalibrationOnlyHelpfulness(t *testing.T) {
	items := []CalibrationItem{
		{CaseID: 1, Annotator: "anna", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true,
			HumanHelpfulness: intPtr(3), JudgeHelpfulness: intPtr(3)},
		{CaseID: 2, Annotator: "anna", HumanVerdict: VerdictFaithful, JudgeHasOpinion: true,
			HumanHelpfulness: intPtr(5), JudgeHelpfulness: intPtr(4)},
	}
	got := ComputeCalibration(155, items, CalibrationOptions{TotalCases: 2})
	if got.Relevance != nil {
		t.Fatalf("没有 relevance 金标时不该有该段, 实际 %+v", got.Relevance)
	}
	if got.Helpfulness == nil || got.Helpfulness.Pairs != 2 {
		t.Fatalf("helpfulness 应有 2 对, 实际 %+v", got.Helpfulness)
	}
	// 人工均值 4.0, judge 均值 3.5: judge 更严格 0.5 分(负数 = judge 更低)
	if got.Helpfulness.Bias != -0.5 {
		t.Fatalf("偏差应为 -0.5, 实际 %.4f", got.Helpfulness.Bias)
	}
	if got.Helpfulness.MAE != 0.5 {
		t.Fatalf("MAE 应为 0.5, 实际 %.4f", got.Helpfulness.MAE)
	}
	if got.Coverage != 1 {
		t.Fatalf("覆盖率应为 1, 实际 %.4f", got.Coverage)
	}
}
