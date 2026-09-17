package eval

import (
	"sort"
	"strconv"
)

// judge 校准计算(M6, 纯函数)。
//
// 要回答的问题不是"judge 准不准"这一句话, 而是三个更具体的:
//  1. **金标本身可信吗** —— 两个人独立打分一致吗(inter-annotator)? 不一致时先别怪 judge;
//  2. **judge 与人工在"有没有幻觉"上一致吗** —— 二分类混淆矩阵 + 一致率 + Cohen's κ;
//  3. **judge 的分数与人工分数差多少、往哪偏** —— MAE / 完全一致率 / ±1 一致率 / 均值偏差。
//
// 为什么用 κ 而不是"一致率": 幻觉率本身很低时(例如 0%), 一个永远说"没有幻觉"的分类器
// 也能拿到 95% 一致率 —— κ 会扣掉"瞎猜也能对"的那部分, 这才是判断 judge 有没有信息量的指标。
//
// 为什么"看不清"要单独算: verdict 三值里的 unclear 既不是 faithful 也不是 hallucinated,
// 把它塞进任何一边都会伪造一致性。这里的做法是**把它排除出二分类样本并报数**,
// 让使用者知道"有多少题因为看不清而没参与校准"。

// 人工 verdict 取值(与 DB 的 CHECK 一致)。
const (
	VerdictFaithful     = "faithful"
	VerdictHallucinated = "hallucinated"
	VerdictUnclear      = "unclear"
)

// MinCalibrationPairs 低于该题数时 κ 很不稳定, 报告里要显式提醒。
const MinCalibrationPairs = 20

// CalibrationItem 一条"人机配对"记录(同一次 run 的同一道题)。
type CalibrationItem struct {
	CaseID    int64
	QID       string
	Annotator string

	// 人工侧
	HumanVerdict     string
	HumanRelevance   *int
	HumanHelpfulness *int
	// Reviewed 表示这条金标被第二个人复核过。金标本身没复核过时, κ 反映的
	// 只是"一个人 vs judge"的口径, 不是"人工 vs judge" —— 报告必须说清这一点。
	Reviewed bool

	// judge 侧: judge_has_opinion=false 表示这次没有判定结论(claims 为空),
	// 不能当成"judge 认为没有幻觉" —— 那正是 M4-2 踩过的坑。
	JudgeHasOpinion  bool
	JudgeUnsupported int
	JudgeRelevance   *int
	JudgeHelpfulness *int
}

// BinaryCalibration 二分类校准(幻觉检测)。
type BinaryCalibration struct {
	Pairs           int     `json:"pairs"`
	JudgePositive   int     `json:"judge_positive"` // judge 说有幻觉
	HumanPositive   int     `json:"human_positive"` // 人工说有幻觉
	TruePositive    int     `json:"true_positive"`
	FalsePositive   int     `json:"false_positive"`
	TrueNegative    int     `json:"true_negative"`
	FalseNegative   int     `json:"false_negative"`
	Agreement       float64 `json:"agreement"`
	Kappa           float64 `json:"kappa"`
	ExcludedUnclear int     `json:"excluded_unclear"` // 人工判"看不清"而没参与校准的题数
}

// ScoreCalibration 有序分数的校准(helpfulness / relevance)。
type ScoreCalibration struct {
	Pairs          int     `json:"pairs"`
	ExactAgreement float64 `json:"exact_agreement"` // 分数完全相同
	Within1        float64 `json:"within_1"`        // 差不超过 1 分
	MAE            float64 `json:"mae"`
	JudgeMean      float64 `json:"judge_mean"`
	HumanMean      float64 `json:"human_mean"`
	Bias           float64 `json:"bias"` // judge - 人工: 正数 = judge 更宽松
	// Confusion 5x5, 行 = 人工, 列 = judge(下标 0 表示 1 分)。
	Confusion [5][5]int `json:"confusion"`
}

// 校准里"judge 判错"的两种错法。
const (
	// DisagreementMissed 人工说有幻觉, judge 说没问题(漏判)。
	DisagreementMissed = "missed"
	// DisagreementFalseAlarm judge 说有幻觉, 人工说没问题(误报)。
	DisagreementFalseAlarm = "false_alarm"
)

// CalibrationDisagreement 一条人机不一致的题。
//
// 为什么光有 κ 不够: κ 只告诉你"judge 行不行", 要**改 prompt** 就得知道"它把哪几道题判错了"。
// 漏判排在误报前面 —— 漏判会让幻觉流到线上, 误报只是多花人工。
type CalibrationDisagreement struct {
	CaseID           int64  `json:"case_id"`
	QID              string `json:"qid,omitempty"`
	Kind             string `json:"kind"`
	HumanVerdict     string `json:"human_verdict"`
	JudgeUnsupported int    `json:"judge_unsupported"`
}

// MaxDisagreements 清单最多给多少条(再多就该去筛 bad case, 而不是在报告里翻页)。
const MaxDisagreements = 50

// InterAnnotator 两个人独立打分之间的一致性(金标可信度的前提)。
type InterAnnotator struct {
	AnnotatorsA    string   `json:"annotators_a"`
	AnnotatorsB    string   `json:"annotators_b"`
	BinaryPairs    int      `json:"binary_pairs"`
	BinaryKappa    *float64 `json:"binary_kappa,omitempty"`
	ScorePairs     int      `json:"score_pairs"`
	ScoreMAE       *float64 `json:"score_mae,omitempty"`
	ScoreExactRate *float64 `json:"score_exact_rate,omitempty"`
}

// CalibrationReport judge 校准报告。
type CalibrationReport struct {
	RunID            int64    `json:"run_id"`
	TotalCases       int      `json:"total_cases"`
	CasesWithGold    int      `json:"cases_with_gold"`
	PrimaryAnnotator string   `json:"primary_annotator"`
	Annotators       []string `json:"annotators"`

	Binary *BinaryCalibration `json:"binary,omitempty"`
	// Helpfulness / Relevance 两份分数校准
	Helpfulness *ScoreCalibration `json:"helpfulness,omitempty"`
	Relevance   *ScoreCalibration `json:"relevance,omitempty"`

	InterAnnotator *InterAnnotator `json:"inter_annotator,omitempty"`

	// Coverage 有金标的题占该 run 全部题的比例(金标覆盖不足时结论不可外推)。
	Coverage float64 `json:"coverage"`

	// GoldJudged / GoldUnjudged: 有金标的题里 judge 给了判定结论 / 没给结论的题数。
	// 分开报出来是为了让"校准样本怎么少了"有据可查 —— 不是数据丢了, 而是这些题的
	// 答案没有可核查断言(claims 为空), judge 对幻觉本来就没有意见。
	GoldJudged   int `json:"gold_judged"`
	GoldUnjudged int `json:"gold_unjudged"`

	// GoldReviewed 这些金标里有多少条经过复核(复核 = 第二个人看过并确认)。
	GoldReviewed int `json:"gold_reviewed"`

	// Disagreements 人机不一致的题(漏判在前), 用于直接去看"judge 错在哪"。
	Disagreements []CalibrationDisagreement `json:"disagreements"`

	// Notes 是给人看的诚实提醒(样本太少 / 金标覆盖不足 / 没有双人打分 / 判定缺失)。
	Notes []string `json:"notes"`
}

type CalibrationOptions struct {
	TotalCases int
}

// ComputeCalibration 计算校准报告。
//
// 多标注员时: **主标注员**取打分最多的那位(并列时取名字序最小), 二分类与分数校准都基于他;
// 两个人之间的一致性单独放在 InterAnnotator 里 —— 这两件事不能混在一起算, 否则
// "两人不一致"会被误读成"judge 不准"。
func ComputeCalibration(runID int64, items []CalibrationItem, opts CalibrationOptions) CalibrationReport {
	report := CalibrationReport{
		RunID:         runID,
		TotalCases:    opts.TotalCases,
		Annotators:    []string{},
		Notes:         []string{},
		Disagreements: []CalibrationDisagreement{},
	}

	byAnnotator := map[string][]CalibrationItem{}
	annotators := make([]string, 0)
	for _, item := range items {
		if _, ok := byAnnotator[item.Annotator]; !ok {
			annotators = append(annotators, item.Annotator)
		}
		byAnnotator[item.Annotator] = append(byAnnotator[item.Annotator], item)
	}
	if len(annotators) == 0 {
		report.Notes = append(report.Notes, "这次 run 还没有人工金标: 先按 M6 的流程挑 30–50 题打分")
		return report
	}
	sort.Strings(annotators)
	report.Annotators = annotators

	primary := annotators[0]
	for _, name := range annotators {
		if len(byAnnotator[name]) > len(byAnnotator[primary]) {
			primary = name
		}
	}
	report.PrimaryAnnotator = primary
	primaryItems := byAnnotator[primary]

	cases := map[int64]bool{}
	for _, item := range primaryItems {
		cases[item.CaseID] = true
		if item.JudgeHasOpinion {
			report.GoldJudged++
		} else {
			report.GoldUnjudged++
		}
		if item.Reviewed {
			report.GoldReviewed++
		}
	}
	report.CasesWithGold = len(cases)
	if opts.TotalCases > 0 {
		report.Coverage = round(float64(report.CasesWithGold)/float64(opts.TotalCases), 4)
	}

	report.Binary = computeBinary(primaryItems)
	report.Disagreements = collectDisagreements(primaryItems)
	report.Helpfulness = computeScores(primaryItems, scoreHelpfulness)
	report.Relevance = computeScores(primaryItems, scoreRelevance)

	if len(annotators) >= 2 {
		report.InterAnnotator = computeInterAnnotator(annotators[0], annotators[1],
			byAnnotator[annotators[0]], byAnnotator[annotators[1]])
	} else {
		report.Notes = append(report.Notes,
			"只有一位标注员: 无法计算人工之间的一致性 —— 金标可信度没有独立证据, κ 的解释要保守")
	}

	if report.Binary != nil && report.Binary.Pairs > 0 && report.Binary.Pairs < MinCalibrationPairs {
		report.Notes = append(report.Notes,
			"参与二分类校准的题数少于 "+strconv.Itoa(MinCalibrationPairs)+" 题: κ 的置信区间很宽, 不要据此下结论")
	}
	if opts.TotalCases > 0 && report.Coverage < 0.5 {
		report.Notes = append(report.Notes, "金标覆盖率低于 50%: 校准结论只能代表被标注的那部分题")
	}
	if report.Binary != nil && report.Binary.ExcludedUnclear > 0 {
		report.Notes = append(report.Notes,
			"有 "+strconv.Itoa(report.Binary.ExcludedUnclear)+" 题人工判为\"看不清\", 已排除出二分类样本(不计入一致率)")
	}
	switch {
	case report.CasesWithGold >= MinCalibrationPairs && report.GoldReviewed == 0:
		// 单人未复核时 κ 只是"个人口径 vs judge": 换个人标一遍结论可能就变了,
		// 所以要把这句话说在报告里, 而不是让人误以为这是"人工"的普遍结论。
		report.Notes = append(report.Notes,
			"所有金标都还没复核: 建议至少抽 20% 双人复核, 否则 κ 只是一个人 vs judge 的口径")
	case report.GoldReviewed > 0:
		report.Notes = append(report.Notes,
			"有 "+strconv.Itoa(report.GoldReviewed)+"/"+strconv.Itoa(report.CasesWithGold)+" 道金标经过复核")
	}
	if missed := countDisagreement(report.Disagreements, DisagreementMissed); missed > 0 {
		report.Notes = append(report.Notes,
			"有 "+strconv.Itoa(missed)+" 题是漏判(人工说有幻觉, judge 说没问题) —— "+
				"这类错会让幻觉流到线上, 优先看它们改 judge prompt")
	}
	if report.GoldUnjudged > 0 {
		report.Notes = append(report.Notes,
			"有 "+strconv.Itoa(report.GoldUnjudged)+" 题 judge 没有给出判定结论"+
				"(答案没有可核查断言, 或这次 run 没开判定): 它们不参与幻觉校准")
	}
	return report
}

// collectDisagreements 挑出人机不一致的题: 漏判在前, 再按 case_id 稳定排序。
//
// 只收"两侧都有意见"的题: 人工判"看不清"或 judge 没有结论的题不算判错 ——
// 那是"没意见", 计进"判错"会冤枉 judge(也会冤枉人)。
func collectDisagreements(items []CalibrationItem) []CalibrationDisagreement {
	out := make([]CalibrationDisagreement, 0)
	for _, item := range items {
		if item.HumanVerdict == VerdictUnclear || !item.JudgeHasOpinion {
			continue
		}
		humanPositive := item.HumanVerdict == VerdictHallucinated
		judgePositive := item.JudgeUnsupported > 0
		if humanPositive == judgePositive {
			continue
		}
		kind := DisagreementFalseAlarm
		if humanPositive {
			kind = DisagreementMissed
		}
		out = append(out, CalibrationDisagreement{
			CaseID:           item.CaseID,
			QID:              item.QID,
			Kind:             kind,
			HumanVerdict:     item.HumanVerdict,
			JudgeUnsupported: item.JudgeUnsupported,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == DisagreementMissed
		}
		return out[i].CaseID < out[j].CaseID
	})
	if len(out) > MaxDisagreements {
		return out[:MaxDisagreements]
	}
	return out
}

// computeBinary 幻觉检测的二分类校准。
func computeBinary(items []CalibrationItem) *BinaryCalibration {
	result := &BinaryCalibration{}
	humanLabels := make([]string, 0, len(items))
	judgeLabels := make([]string, 0, len(items))

	for _, item := range items {
		if item.HumanVerdict == VerdictUnclear {
			result.ExcludedUnclear++
			continue
		}
		if !item.JudgeHasOpinion {
			// 没有判定结论: 不能算成"judge 认为没有幻觉"(M4-2 的教训), 直接不进样本
			continue
		}
		humanPositive := item.HumanVerdict == VerdictHallucinated
		judgePositive := item.JudgeUnsupported > 0

		result.Pairs++
		if judgePositive {
			result.JudgePositive++
		}
		if humanPositive {
			result.HumanPositive++
		}
		switch {
		case humanPositive && judgePositive:
			result.TruePositive++
		case !humanPositive && judgePositive:
			result.FalsePositive++
		case !humanPositive && !judgePositive:
			result.TrueNegative++
		default:
			result.FalseNegative++
		}
		humanLabels = append(humanLabels, verdictLabel(humanPositive))
		judgeLabels = append(judgeLabels, verdictLabel(judgePositive))
	}

	if result.Pairs == 0 {
		return nil
	}
	result.Agreement = round(float64(result.TruePositive+result.TrueNegative)/float64(result.Pairs), 4)
	result.Kappa = round(cohenKappa(humanLabels, judgeLabels), 4)
	return result
}

type scorePicker func(CalibrationItem) (*int, *int)

func scoreHelpfulness(item CalibrationItem) (*int, *int) {
	return item.HumanHelpfulness, item.JudgeHelpfulness
}

func scoreRelevance(item CalibrationItem) (*int, *int) {
	return item.HumanRelevance, item.JudgeRelevance
}

// computeScores 有序分数(1–5)的校准: 完全一致率 / ±1 一致率 / MAE / 均值偏差 / 5×5 混淆矩阵。
// 只统计"两侧都打了分"的题: 一侧没打分不是 0 分, 是"没有意见"。
func computeScores(items []CalibrationItem, pick scorePicker) *ScoreCalibration {
	result := &ScoreCalibration{}
	humanSum, judgeSum := 0, 0
	for _, item := range items {
		human, judge := pick(item)
		if human == nil || judge == nil {
			continue
		}
		if *human < 1 || *human > 5 || *judge < 1 || *judge > 5 {
			continue
		}
		result.Pairs++
		humanSum += *human
		judgeSum += *judge
		diff := *judge - *human
		if diff < 0 {
			diff = -diff
		}
		if diff == 0 {
			result.ExactAgreement++
		}
		if diff <= 1 {
			result.Within1++
		}
		result.MAE += float64(diff)
		result.Confusion[*human-1][*judge-1]++
	}
	if result.Pairs == 0 {
		return nil
	}
	total := float64(result.Pairs)
	result.MAE = round(result.MAE/total, 4)
	result.ExactAgreement = round(float64(result.ExactAgreement)/total, 4)
	result.Within1 = round(float64(result.Within1)/total, 4)
	result.HumanMean = round(float64(humanSum)/total, 4)
	result.JudgeMean = round(float64(judgeSum)/total, 4)
	result.Bias = round(result.JudgeMean-result.HumanMean, 4)
	return result
}

// computeInterAnnotator 两位标注员之间的一致性(同一 (run, case) 两人都打过分的部分)。
func computeInterAnnotator(
	nameA, nameB string, itemsA, itemsB []CalibrationItem,
) *InterAnnotator {
	byCaseB := map[int64]CalibrationItem{}
	for _, item := range itemsB {
		byCaseB[item.CaseID] = item
	}

	result := &InterAnnotator{AnnotatorsA: nameA, AnnotatorsB: nameB}
	labelsA := make([]string, 0)
	labelsB := make([]string, 0)
	scoreDiffSum := 0.0
	scoreExact := 0
	for _, itemA := range itemsA {
		itemB, ok := byCaseB[itemA.CaseID]
		if !ok {
			continue
		}
		if itemA.HumanVerdict != VerdictUnclear && itemB.HumanVerdict != VerdictUnclear {
			labelsA = append(labelsA, verdictLabel(itemA.HumanVerdict == VerdictHallucinated))
			labelsB = append(labelsB, verdictLabel(itemB.HumanVerdict == VerdictHallucinated))
		}
		if itemA.HumanHelpfulness != nil && itemB.HumanHelpfulness != nil {
			result.ScorePairs++
			diff := *itemA.HumanHelpfulness - *itemB.HumanHelpfulness
			if diff < 0 {
				diff = -diff
			}
			scoreDiffSum += float64(diff)
			if diff == 0 {
				scoreExact++
			}
		}
	}

	// 至少 2 对才有意义: 只重合 1 题算出来的"一致性"是噪声
	if len(labelsA) >= 2 {
		kappa := round(cohenKappa(labelsA, labelsB), 4)
		result.BinaryPairs = len(labelsA)
		result.BinaryKappa = &kappa
	}
	if result.ScorePairs >= 2 {
		mae := round(scoreDiffSum/float64(result.ScorePairs), 4)
		exact := round(float64(scoreExact)/float64(result.ScorePairs), 4)
		result.ScoreMAE = &mae
		result.ScoreExactRate = &exact
	}
	if result.BinaryPairs == 0 && result.ScorePairs == 0 {
		return nil
	}
	return result
}

// cohenKappa Cohen's κ: 扣掉"瞎猜也能对"的部分。
//
// po = 观察到的一致率; pe = 按两侧各自的边际分布算出的期望一致率;
// κ = (po - pe) / (1 - pe)。pe == 1(两侧都只用一个类别)时 κ 无定义, 返回 0。
func cohenKappa(labelsA, labelsB []string) float64 {
	if len(labelsA) == 0 || len(labelsA) != len(labelsB) {
		return 0
	}
	total := float64(len(labelsA))
	countA := map[string]float64{}
	countB := map[string]float64{}
	observed := 0.0
	for index := range labelsA {
		countA[labelsA[index]]++
		countB[labelsB[index]]++
		if labelsA[index] == labelsB[index] {
			observed++
		}
	}
	po := observed / total
	pe := 0.0
	for label, count := range countA {
		pe += (count / total) * (countB[label] / total)
	}
	if pe >= 1 {
		return 0 // 没有任何变异: κ 无定义
	}
	return (po - pe) / (1 - pe)
}

// countDisagreement 数某一类判错多少条(用于提示语)。
func countDisagreement(items []CalibrationDisagreement, kind string) int {
	count := 0
	for _, item := range items {
		if item.Kind == kind {
			count++
		}
	}
	return count
}

func verdictLabel(positive bool) string {
	if positive {
		return "positive"
	}
	return "negative"
}
