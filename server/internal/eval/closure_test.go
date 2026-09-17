package eval

import (
	"strings"
	"testing"
)

func closureCase(caseID int64, qid string, recall float64, flags ...string) ClosureCase {
	return ClosureCase{
		CaseID:  caseID,
		QID:     qid,
		Metrics: map[string]float64{"recall": recall, "reciprocal_rank": recall, "hit": hitOf(recall), "precision": 0.2},
		Flags:   flags,
	}
}

func hitOf(recall float64) float64 {
	if recall > 0 {
		return 1
	}
	return 0
}

func closureAnnotation(annotationID, caseID int64, status string) ClosureAnnotation {
	return ClosureAnnotation{AnnotationID: annotationID, CaseID: caseID, Status: status}
}

// TestComputeClosureVerdicts 五种结局各一题, 逐项核对(这是闭环页的全部结论来源)。
func TestComputeClosureVerdicts(t *testing.T) {
	annotations := []ClosureAnnotation{
		closureAnnotation(1, 61, "fixed"), // 修好了 -> 可销单
		closureAnnotation(2, 62, "fixed"), // 标签没变
		closureAnnotation(3, 63, "open"),  // 反而变坏
		closureAnnotation(4, 64, "open"),  // 换了病
		closureAnnotation(5, 65, "fixed"), // 候选 run 里没有
	}
	baseline := []ClosureCase{
		closureCase(61, "zjc-027", 0.5, "retrieval_low_rank"),
		closureCase(62, "zjc-028", 0, "retrieval_miss"),
		closureCase(63, "zjc-029", 1),
		closureCase(64, "zjc-030", 0.5, "retrieval_partial"),
		closureCase(65, "zjc-031", 0, "retrieval_miss"),
	}
	candidate := []ClosureCase{
		closureCase(61, "zjc-027", 1),
		closureCase(62, "zjc-028", 0, "retrieval_miss"),
		closureCase(63, "zjc-029", 1, "hallucination"),
		closureCase(64, "zjc-030", 0.5, "retrieval_low_rank"),
		// 65 缺席
	}

	report := ComputeClosure(annotations, baseline, candidate, ClosureOptions{
		BaselineHash: "712f6b17", CandidateHash: "4e767020",
		BaselineAttribution: true, CandidateAttribution: true,
	})

	if !report.Comparable || report.SameConfig {
		t.Fatalf("两次配置不同, 应可比且非复现: %+v", report)
	}
	summary := report.Summary
	if summary.Annotated != 5 {
		t.Fatalf("应统计 5 道被标注的题, 实际 %d", summary.Annotated)
	}
	if summary.Improved != 1 || summary.Stable != 1 || summary.Worsened != 1 ||
		summary.Changed != 1 || summary.Unverifiable != 1 {
		t.Fatalf("五种结局应各 1: %+v", summary)
	}
	if summary.FixedTotal != 3 || summary.EligibleForVerify != 1 {
		t.Fatalf("fixed 3 道里应只有 1 道可销单: %+v", summary)
	}
	if summary.ByStatus["fixed"] != 3 || summary.ByStatus["open"] != 2 {
		t.Fatalf("状态分布不对: %+v", summary.ByStatus)
	}

	byQID := map[string]ClosureRecord{}
	for _, record := range report.Records {
		byQID[record.QID] = record
	}
	if len(byQID) != 5 {
		t.Fatalf("应有 5 条记录, 实际 %d", len(byQID))
	}

	improved := byQID["zjc-027"]
	if improved.Verdict != ClosureImproved || !improved.EligibleForVerify {
		t.Fatalf("修好且状态 fixed 应可销单: %+v", improved)
	}
	if improved.BlockedReason != "" {
		t.Fatalf("可销单时不该有阻塞原因: %q", improved.BlockedReason)
	}
	// 证据: recall 0.5 -> 1 是 better, precision 0.2 -> 0.2 落在噪声内是 same
	recall := evidenceOf(t, improved, "recall")
	if recall.Direction != EvidenceBetter || recall.Left != 0.5 || recall.Right != 1 {
		t.Fatalf("recall 证据不对: %+v", recall)
	}
	if precision := evidenceOf(t, improved, "precision"); precision.Direction != EvidenceSame {
		t.Fatalf("0.2 -> 0.2 应落在噪声内(same): %+v", precision)
	}

	stable := byQID["zjc-028"]
	if stable.Verdict != ClosureStable || stable.EligibleForVerify {
		t.Fatalf("标签没变就不该可销单: %+v", stable)
	}
	if !strings.Contains(stable.BlockedReason, "retrieval_miss") {
		t.Fatalf("要说清还是哪个标签: %q", stable.BlockedReason)
	}

	worsened := byQID["zjc-029"]
	if worsened.Verdict != ClosureWorsened || worsened.EligibleForVerify {
		t.Fatalf("原本干净变脏 = 变坏: %+v", worsened)
	}
	if !strings.Contains(worsened.BlockedReason, "弄坏") {
		t.Fatalf("变坏的原因要说清: %q", worsened.BlockedReason)
	}

	changed := byQID["zjc-030"]
	if changed.Verdict != ClosureChanged {
		t.Fatalf("主因换了不算修好: %+v", changed)
	}
	if !strings.Contains(changed.BlockedReason, "retrieval_partial") ||
		!strings.Contains(changed.BlockedReason, "retrieval_low_rank") {
		t.Fatalf("换了病要并排给出前后标签: %q", changed.BlockedReason)
	}

	missing := byQID["zjc-031"]
	if missing.Verdict != ClosureUnverifiable {
		t.Fatalf("候选 run 没这道题应不可判断: %+v", missing)
	}
	if !strings.Contains(missing.BlockedReason, "没有这道题") {
		t.Fatalf("不可判断的原因要说清: %q", missing.BlockedReason)
	}

	// 排序: 可销单在最前
	if report.Records[0].QID != "zjc-027" {
		t.Fatalf("可销单的题应排在最前, 实际 %s(%s)", report.Records[0].QID, report.Records[0].Verdict)
	}
}

// TestComputeClosureOpenButImproved 变好了但状态还是 open: 不能销单, 且要说明为什么。
func TestComputeClosureOpenButImproved(t *testing.T) {
	report := ComputeClosure(
		[]ClosureAnnotation{closureAnnotation(1, 61, "open")},
		[]ClosureCase{closureCase(61, "zjc-027", 0, "retrieval_miss")},
		[]ClosureCase{closureCase(61, "zjc-027", 1)},
		ClosureOptions{BaselineAttribution: true, CandidateAttribution: true,
			BaselineHash: "a", CandidateHash: "b"},
	)
	record := report.Records[0]
	if record.Verdict != ClosureImproved {
		t.Fatalf("结局应是 improved: %+v", record)
	}
	if record.EligibleForVerify || report.Summary.EligibleForVerify != 0 {
		t.Fatalf("open 的题不该出现在销单清单里: %+v", record)
	}
	if !strings.Contains(record.BlockedReason, "先标 fixed") {
		t.Fatalf("要说清下一步动作: %q", record.BlockedReason)
	}
}

// TestComputeClosureAttributionMismatch 候选 run 没做过归因: 不能给出"全修好了"的假结论。
// 这是最危险的假阳性 —— 会让人把没修的题全部销单。
func TestComputeClosureAttributionMismatch(t *testing.T) {
	report := ComputeClosure(
		[]ClosureAnnotation{
			closureAnnotation(1, 61, "fixed"),
			closureAnnotation(2, 62, "fixed"),
		},
		[]ClosureCase{
			closureCase(61, "zjc-027", 0, "retrieval_miss"),
			closureCase(62, "zjc-028", 0, "retrieval_miss"),
		},
		[]ClosureCase{
			closureCase(61, "zjc-027", 1), // flags 空 = 没做过归因, 不是"修好了"
			closureCase(62, "zjc-028", 1),
		},
		ClosureOptions{BaselineAttribution: true, CandidateAttribution: false,
			BaselineHash: "a", CandidateHash: "b"},
	)

	if report.Comparable {
		t.Fatal("归因准备度不一致时应整体判为不可比较")
	}
	if !strings.Contains(report.Reason, "make attribution") {
		t.Fatalf("要给可执行的补救指令: %q", report.Reason)
	}
	if report.Summary.Improved != 0 || report.Summary.EligibleForVerify != 0 {
		t.Fatalf("不可比较时绝不能报\"修好了\": %+v", report.Summary)
	}
	if report.Summary.Unverifiable != 2 {
		t.Fatalf("两题都应记为不可判断: %+v", report.Summary)
	}
	for _, record := range report.Records {
		if !strings.Contains(record.BlockedReason, "归因") {
			t.Fatalf("要说清为什么判断不了: %q", record.BlockedReason)
		}
	}
}

// TestComputeClosureSameConfig 两次 run 指纹相同: 是复现不是实验, 必须在报告里说明。
func TestComputeClosureSameConfig(t *testing.T) {
	report := ComputeClosure(
		[]ClosureAnnotation{closureAnnotation(1, 61, "fixed")},
		[]ClosureCase{closureCase(61, "zjc-027", 0.5, "retrieval_miss")},
		[]ClosureCase{closureCase(61, "zjc-027", 0.5, "retrieval_miss")},
		ClosureOptions{BaselineAttribution: true, CandidateAttribution: true,
			BaselineHash: "4e767020", CandidateHash: "4e767020"},
	)
	if !report.SameConfig {
		t.Fatal("指纹相同应标记 SameConfig")
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "复现而不是实验") {
		t.Fatalf("要说清这是复现: %v", report.Notes)
	}
}

// TestComputeClosureEmptyAnnotations 基线还没标注过: 报告说清下一步, 而不是给一张空表。
func TestComputeClosureEmptyAnnotations(t *testing.T) {
	report := ComputeClosure(nil,
		[]ClosureCase{closureCase(61, "zjc-027", 1)},
		[]ClosureCase{closureCase(61, "zjc-027", 1)},
		ClosureOptions{BaselineHash: "a", CandidateHash: "b"},
	)
	if report.Summary.Annotated != 0 || len(report.Records) != 0 {
		t.Fatalf("没有标注不该有记录: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "还没有任何人工标注") {
		t.Fatalf("要说清先去标注: %v", report.Notes)
	}
}

// TestComputeClosureStatusFilter 只看 fixed 时, 分母与清单都只算 fixed。
func TestComputeClosureStatusFilter(t *testing.T) {
	annotations := []ClosureAnnotation{
		closureAnnotation(1, 61, "fixed"),
		closureAnnotation(2, 62, "open"),
	}
	baseline := []ClosureCase{
		closureCase(61, "zjc-027", 0, "retrieval_miss"),
		closureCase(62, "zjc-028", 0, "retrieval_miss"),
	}
	candidate := []ClosureCase{closureCase(61, "zjc-027", 1), closureCase(62, "zjc-028", 1)}
	report := ComputeClosure(annotations, baseline, candidate, ClosureOptions{
		StatusFilter: "fixed", BaselineAttribution: true, CandidateAttribution: true,
		BaselineHash: "a", CandidateHash: "b",
	})
	if report.Summary.Annotated != 1 || len(report.Records) != 1 {
		t.Fatalf("只看 fixed 应只有 1 条: %+v", report.Summary)
	}
	if report.Summary.FixedTotal != 1 || report.Summary.EligibleForVerify != 1 {
		t.Fatalf("fixed 1 道且修好 -> 可销单 1: %+v", report.Summary)
	}

	empty := ComputeClosure(annotations, baseline, candidate, ClosureOptions{
		StatusFilter: "wontfix", BaselineAttribution: true, CandidateAttribution: true,
	})
	if !strings.Contains(strings.Join(empty.Notes, "\n"), "wontfix") {
		t.Fatalf("没有该状态的标注时要说明: %v", empty.Notes)
	}
}

// TestComputeClosureEvidenceGeneration 生成侧证据: 缺判定的题必须标不可比, 不能当 0 分。
func TestComputeClosureEvidenceGeneration(t *testing.T) {
	left := ClosureCase{
		CaseID: 61, QID: "zjc-027",
		Metrics: map[string]float64{"recall": 1},
		Judge:   &ABJudge{Claims: 4, Supported: 2, Unsupported: 2, HasRubric: true, Helpfulness: 2, Relevance: 3},
	}
	right := ClosureCase{
		CaseID: 61, QID: "zjc-027",
		Metrics: map[string]float64{"recall": 1},
		Judge:   &ABJudge{Claims: 4, Supported: 4, Unsupported: 0, HasRubric: true, Helpfulness: 5, Relevance: 5},
	}
	evidence := closureEvidence(left, right, closureSpecs(nil), 0)

	hallucination := evidenceOfList(t, evidence, "hallucination_rate")
	if !hallucination.Comparable || hallucination.Left != 0.5 || hallucination.Right != 0 {
		t.Fatalf("幻觉率应 0.5 -> 0: %+v", hallucination)
	}
	// 幻觉率越低越好: 下降是 better(方向判断错了会把"修好"涂成红色)
	if hallucination.Direction != EvidenceBetter {
		t.Fatalf("幻觉率下降应为 better: %+v", hallucination)
	}
	if helpfulness := evidenceOfList(t, evidence, "helpfulness"); helpfulness.Direction != EvidenceBetter {
		t.Fatalf("helpfulness 2 -> 5 应为 better: %+v", helpfulness)
	}

	// 右侧没有判定 -> 生成侧全部不可比
	noJudge := closureEvidence(left, ClosureCase{CaseID: 61, QID: "zjc-027",
		Metrics: map[string]float64{"recall": 1}}, closureSpecs(nil), 0)
	uncomparable := evidenceOfList(t, noJudge, "hallucination_rate")
	if uncomparable.Comparable {
		t.Fatalf("一侧没判定时应标不可比(而不是 0): %+v", uncomparable)
	}
	if uncomparable.Direction != EvidenceSame {
		t.Fatalf("不可比的证据方向应为 same: %+v", uncomparable)
	}
}

// TestComputeClosureAnnotatedCaseMissingInBaseline 标注挂在一道基线 run 里不存在的题上:
// 不进闭环样本(否则会计入分母、拉低"修好率")。
func TestComputeClosureAnnotatedCaseMissingInBaseline(t *testing.T) {
	report := ComputeClosure(
		[]ClosureAnnotation{closureAnnotation(1, 999, "fixed")},
		[]ClosureCase{closureCase(61, "zjc-027", 1)},
		[]ClosureCase{closureCase(61, "zjc-027", 1)},
		ClosureOptions{BaselineAttribution: true, CandidateAttribution: true},
	)
	if report.Summary.Annotated != 0 || len(report.Records) != 0 {
		t.Fatalf("基线里没有这道题, 不该进样本: %+v", report)
	}
}

// TestFlagTransition 共享的转移判定(A/B 与闭环都用它, 所以单独钉住)。
func TestFlagTransition(t *testing.T) {
	cases := []struct {
		left, right []string
		want        string
	}{
		{nil, nil, TransitionStable},
		{[]string{"retrieval_miss"}, nil, TransitionFixed},
		{nil, []string{"hallucination"}, TransitionBroke},
		{[]string{"retrieval_partial"}, []string{"retrieval_low_rank"}, TransitionChanged},
		{[]string{"retrieval_miss"}, []string{"retrieval_miss"}, TransitionStable},
		// 标签集合相同但顺序不同: 不算变化(sameFlags 会排序后比)
		{[]string{"a", "b"}, []string{"b", "a"}, TransitionStable},
	}
	for _, item := range cases {
		if got := FlagTransition(item.left, item.right); got != item.want {
			t.Fatalf("%v -> %v: got %q, want %q", item.left, item.right, got, item.want)
		}
	}
}

func evidenceOf(t *testing.T, record ClosureRecord, metric string) ClosureEvidence {
	t.Helper()
	return evidenceOfList(t, record.Evidence, metric)
}

func evidenceOfList(t *testing.T, evidence []ClosureEvidence, metric string) ClosureEvidence {
	t.Helper()
	for _, item := range evidence {
		if item.Metric == metric {
			return item
		}
	}
	t.Fatalf("证据里缺少指标 %s: %+v", metric, evidence)
	return ClosureEvidence{}
}
