package eval

import (
	"math"
	"sort"
	"strconv"
)

// 标注闭环计算(M6-4, 纯函数)。
//
// 它回答 M6 的最后一个问题: **"我标了 fixed 的题, 在新一次 run 里真的变好了吗?"**
// M5-1 的 A/B 报告回答的是"两次 run 谁赢"(整体视角); 闭环回答的是
// "我处理过的那几条待办, 现在能不能销单"(逐条视角) —— 后者才是迭代真正推进的地方。
//
// 三条设计纪律:
//  1. **"变好"的定义只有一处**: 复用 FlagTransition(与 A/B 同一份), 不另写一遍 ——
//     否则会出现"对比页说修好了、闭环页说没修好"这种最伤信任的分歧;
//  2. **证据要并排给出**: 每条记录都带指标级证据(左值/右值/方向), 让人能自己核对,
//     而不是只吃一个"已修好"的结论;
//  3. **不能销单就说清为什么**: 仍然是同样的标签 / 指标没动 / 这次 run 里没这道题,
//     三种"没变好"的原因要分开讲, 否则标注员只能瞎猜。

// 闭环结局(每道被标注过的题)。
const (
	// ClosureImproved 左有问题 -> 右干净: 这才是"可以销单"的状态。
	ClosureImproved = "improved"
	// ClosureStable 两侧标签一致(含都干净、也都坏且主因相同)。
	ClosureStable = "stable"
	// ClosureWorsened 左干净 -> 右出问题: 这次改动把没坏的题弄坏了, 比"没修好"更严重。
	ClosureWorsened = "worsened"
	// ClosureChanged 两侧都有问题但主因变了(例如 miss -> low_rank): 换了个病, 不算修好。
	ClosureChanged = "changed"
	// ClosureUnverifiable 这次 run 里没有这道题, 或两侧归因准备度不一致 —— 无法判断。
	ClosureUnverifiable = "unverifiable"
)

// 指标证据的方向。
const (
	EvidenceBetter = "better"
	EvidenceWorse  = "worse"
	EvidenceSame   = "same"
)

// ClosureAnnotation 基线 run 上的一条人工标注(闭环的"待办清单")。
type ClosureAnnotation struct {
	AnnotationID int64
	CaseID       int64
	Status       string
	Reason       string
	Comment      string
	Assignee     string
}

// ClosureCase 一次 run 里某道题参与闭环判断的部分(带 case_id, 便于与标注对齐)。
//
// 注意不能直接用 ABCase: 那边按 qid 对齐(A/B 是题库视角), 而标注是按 case_id 存的。
type ClosureCase struct {
	CaseID   int64
	QID      string
	Question string
	Metrics  map[string]float64
	Flags    []string
	Judge    *ABJudge
}

// ClosureEvidence 单指标证据。
type ClosureEvidence struct {
	Metric         string  `json:"metric"`
	Left           float64 `json:"left"`
	Right          float64 `json:"right"`
	Delta          float64 `json:"delta"`
	HigherIsBetter bool    `json:"higher_is_better"`
	Direction      string  `json:"direction"`
	// Comparable=false 表示某一侧不可比(例如缺判定), 此时数值无意义, 前端显示 "—"。
	Comparable bool `json:"comparable"`
}

// ClosureRecord 一条标注的闭环结局。
type ClosureRecord struct {
	AnnotationID int64  `json:"annotation_id"`
	CaseID       int64  `json:"case_id"`
	QID          string `json:"qid"`
	Question     string `json:"question,omitempty"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	Comment      string `json:"comment"`
	Assignee     string `json:"assignee"`

	Verdict    string   `json:"verdict"`
	FlagsLeft  []string `json:"flags_left"`
	FlagsRight []string `json:"flags_right"`

	// Evidence 指标级证据(可比的全部列出, 前端按方向挑重点展示)。
	Evidence []ClosureEvidence `json:"evidence"`

	// EligibleForVerify: 状态是 fixed 且这次确实变好了 —— 前端可以一键推进到 verified。
	EligibleForVerify bool `json:"eligible_for_verify"`
	// BlockedReason 非空表示"为什么现在还不能销单"(即使 eligible, 也可能为空)。
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// ClosureSummary 汇总。
type ClosureSummary struct {
	Annotated int `json:"annotated"`
	Improved  int `json:"improved"`
	Stable    int `json:"stable"`
	Worsened  int `json:"worsened"`
	Changed   int `json:"changed"`
	// Unverifiable 这次 run 里没有 / 判断不了。
	Unverifiable int `json:"unverifiable"`
	// ByStatus 基线标注的状态分布(让人知道这份闭环覆盖了多少待办)。
	ByStatus map[string]int `json:"by_status"`
	// EligibleForVerify 可以直接销单(fixed + 确实变好)的题数。
	EligibleForVerify int `json:"eligible_for_verify"`
	// FixedTotal 基线上状态为 fixed 的题数(分母: "已修 N 题中 M 题真的好了")。
	FixedTotal int `json:"fixed_total"`
}

// ClosureReport 闭环报告(直接作为 API 响应体)。
type ClosureReport struct {
	Baseline  int64 `json:"baseline"`
	Candidate int64 `json:"candidate"`

	Comparable bool   `json:"comparable"`
	Reason     string `json:"reason,omitempty"`

	BaselineHash  string `json:"baseline_hash"`
	CandidateHash string `json:"candidate_hash"`
	// SameConfig=true 表示两次 run 配置指纹相同: 这是复现, 不是实验 ——
	// 此时"变好/变坏"只可能是跨 run 噪声或非确定性, 页面必须先把这句话说在前面。
	SameConfig bool `json:"same_config"`

	// AttributionMissing 非空表示两侧归因准备度不一致, 标签层面的闭环判断不可信。
	AttributionMissing string `json:"attribution_missing,omitempty"`

	Summary ClosureSummary  `json:"summary"`
	Records []ClosureRecord `json:"records"`
	Notes   []string        `json:"notes"`
}

// ClosureOptions 闭环参数。
type ClosureOptions struct {
	NoiseFloor float64
	// Metrics 参与证据展示的指标(空 = 检索 4 项 + 生成 5 项)。
	Metrics []string
	// BaselineAttribution / CandidateAttribution: 该 run 的 flags 是否由归因规则写过。
	BaselineAttribution  bool
	CandidateAttribution bool
	BaselineHash         string
	CandidateHash        string
	// StatusFilter 只看某个标注状态(空 = 全部)。
	StatusFilter string
}

// ComputeClosure 把"基线标注 + 两次 run 的单题结果"拼成闭环报告。
//
// 只统计**两次 run 都有的题**(按 case_id): 候选 run 里没有这道题时无法判断,
// 记为 unverifiable —— 那通常意味着评测集变了, 本身就是要紧的信号。
func ComputeClosure(
	annotations []ClosureAnnotation,
	baseline, candidate []ClosureCase,
	opts ClosureOptions,
) ClosureReport {
	report := ClosureReport{
		BaselineHash:  opts.BaselineHash,
		CandidateHash: opts.CandidateHash,
		SameConfig:    opts.BaselineHash != "" && opts.BaselineHash == opts.CandidateHash,
		Comparable:    true,
		Summary: ClosureSummary{
			ByStatus: map[string]int{},
		},
		Records: []ClosureRecord{},
		Notes:   []string{},
	}

	mismatch := closureAttributionMismatch(opts)
	report.AttributionMissing = mismatch
	if mismatch != "" {
		// 一侧没做过归因时, 它的 flags 是空的 -> 每道有标签的题都会被算成"修好了"。
		// 这种假阳性比不给结论更糟(会让人把没修的题销单), 所以整体降级为不可判断。
		report.Comparable = false
		report.Reason = mismatch
	}

	if report.SameConfig {
		report.Notes = append(report.Notes,
			"两次 run 的配置指纹完全相同: 这是复现而不是实验, 出现变化只可能是噪声或非确定性 —— "+
				"要验证修复效果, 请选一次真的改了配置的 run")
	}

	baselineByCase := make(map[int64]ClosureCase, len(baseline))
	for _, item := range baseline {
		baselineByCase[item.CaseID] = item
	}
	candidateByCase := make(map[int64]ClosureCase, len(candidate))
	for _, item := range candidate {
		candidateByCase[item.CaseID] = item
	}

	specs := closureSpecs(opts.Metrics)
	filtered := 0
	for _, annotation := range annotations {
		if opts.StatusFilter != "" && annotation.Status != opts.StatusFilter {
			continue
		}
		leftCase, ok := baselineByCase[annotation.CaseID]
		if !ok {
			// 标注挂在一个已经不存在的 case 上(数据被删/换集) —— 不当成闭环样本
			continue
		}
		filtered++
		report.Summary.ByStatus[annotation.Status]++
		if annotation.Status == "fixed" {
			report.Summary.FixedTotal++
		}

		record := ClosureRecord{
			AnnotationID: annotation.AnnotationID,
			CaseID:       annotation.CaseID,
			QID:          leftCase.QID,
			Question:     leftCase.Question,
			Status:       annotation.Status,
			Reason:       annotation.Reason,
			Comment:      annotation.Comment,
			Assignee:     annotation.Assignee,
			FlagsLeft:    append([]string{}, leftCase.Flags...),
			FlagsRight:   []string{},
			Evidence:     []ClosureEvidence{},
		}

		rightCase, present := candidateByCase[annotation.CaseID]
		if !present {
			record.Verdict = ClosureUnverifiable
			record.BlockedReason = "这次 run 里没有这道题(评测集可能变了), 无法判断是否修好"
			record.FlagsRight = nil
			report.Summary.Unverifiable++
			report.Records = append(report.Records, record)
			continue
		}
		record.FlagsRight = append([]string{}, rightCase.Flags...)
		record.Evidence = closureEvidence(leftCase, rightCase, specs, opts.NoiseFloor)

		if mismatch != "" {
			// 归因不可信: 连"有没有标签"都不能看, 只能报"判断不了"
			record.Verdict = ClosureUnverifiable
			record.BlockedReason = "两侧归因准备度不一致, 标签对比不可信(见页面顶部提示)"
			report.Summary.Unverifiable++
			report.Records = append(report.Records, record)
			continue
		}

		switch FlagTransition(leftCase.Flags, rightCase.Flags) {
		case TransitionFixed:
			record.Verdict = ClosureImproved
			report.Summary.Improved++
		case TransitionBroke:
			record.Verdict = ClosureWorsened
			record.BlockedReason = "这次改动把原本干净的题弄坏了: " + flagText(rightCase.Flags)
			report.Summary.Worsened++
		case TransitionChanged:
			record.Verdict = ClosureChanged
			record.BlockedReason = "病换了但没好: " + flagText(leftCase.Flags) + " → " + flagText(rightCase.Flags)
			report.Summary.Changed++
		default:
			record.Verdict = ClosureStable
			if len(rightCase.Flags) > 0 {
				record.BlockedReason = "仍然有同样的标签: " + flagText(rightCase.Flags)
			} else {
				record.BlockedReason = "两次 run 里都是干净的, 没有可验证的改善(标注可能标错了)"
			}
			report.Summary.Stable++
		}

		// 销单只认"状态是 fixed + 这次确实变好": open 的题先标 fixed 再谈验证,
		// 否则 verified 会失去"M6 闭环走完"的含义(D20)。
		if record.Verdict == ClosureImproved && annotation.Status == "fixed" {
			record.EligibleForVerify = true
			record.BlockedReason = ""
			report.Summary.EligibleForVerify++
		} else if record.Verdict == ClosureImproved && annotation.Status == "open" {
			record.BlockedReason = "确实变好了, 但状态还是 open: 先标 fixed 再走验证, 否则 verified 失去含义"
		}
		report.Records = append(report.Records, record)
	}

	report.Summary.Annotated = filtered
	sortClosureRecords(report.Records)

	if filtered == 0 {
		if opts.StatusFilter != "" {
			report.Notes = append(report.Notes, "这次 run 上没有状态为 "+opts.StatusFilter+" 的标注")
		} else {
			report.Notes = append(report.Notes,
				"基线 run 还没有任何人工标注: 先去标注工作台把 bad case 标成 fixed, 再来这里看是否真的修好")
		}
	}
	if report.Summary.Worsened > 0 {
		report.Notes = append(report.Notes,
			"有 "+strconv.Itoa(report.Summary.Worsened)+" 道题反而变坏了 —— 这类回归比\"没修好\"更要紧, 先看它们")
	}
	if report.Summary.Unverifiable > 0 {
		report.Notes = append(report.Notes,
			"有 "+strconv.Itoa(report.Summary.Unverifiable)+" 道题无法判断(候选 run 里没有 / 归因不可信)")
	}
	return report
}

// closureAttributionMismatch 与 A/B 同一套判断(措辞也一致): 只有一侧缺失才算问题。
func closureAttributionMismatch(opts ClosureOptions) string {
	return attributionMismatch(ABOptions{
		LeftAttribution:  opts.BaselineAttribution,
		RightAttribution: opts.CandidateAttribution,
	})
}

// closureSpecs 证据用的指标取值器: 检索 4 项 + 生成 5 项(生成侧缺判定会自动不可比)。
func closureSpecs(names []string) []MetricSpec {
	if len(names) == 0 {
		return append(RetrievalMetrics(), GenerationMetrics()...)
	}
	return retrievalSpecs(names)
}

// closureEvidence 逐指标给出 左值/右值/方向。方向带噪声底: 差值落在噪声里一律 same,
// 与 A/B 报告同一条纪律(跨 run 抖动不宣称变化)。
func closureEvidence(left, right ClosureCase, specs []MetricSpec, noiseFloor float64) []ClosureEvidence {
	if noiseFloor <= 0 {
		noiseFloor = DefaultNoiseFloor
	}
	out := make([]ClosureEvidence, 0, len(specs))
	for _, spec := range specs {
		leftValue, leftOK := spec.Value(asABCase(left))
		rightValue, rightOK := spec.Value(asABCase(right))
		evidence := ClosureEvidence{
			Metric:         spec.Name,
			HigherIsBetter: spec.HigherIsBetter,
			Comparable:     leftOK && rightOK,
			Direction:      EvidenceSame,
		}
		if !evidence.Comparable {
			// 一侧不可比(例如缺判定): 数值不展示, 免得"没判定"被读成 0
			out = append(out, evidence)
			continue
		}
		evidence.Left = round(leftValue, 4)
		evidence.Right = round(rightValue, 4)
		evidence.Delta = round(rightValue-leftValue, 4)
		switch {
		case math.Abs(rightValue-leftValue) <= noiseFloor:
			evidence.Direction = EvidenceSame
		case (rightValue > leftValue) == spec.HigherIsBetter:
			evidence.Direction = EvidenceBetter
		default:
			evidence.Direction = EvidenceWorse
		}
		out = append(out, evidence)
	}
	return out
}

// asABCase 复用 A/B 的指标取值器(它们只读 Metrics/Judge), 免得为 M6 再写一遍口径。
func asABCase(item ClosureCase) ABCase {
	return ABCase{QID: item.QID, Metrics: item.Metrics, Flags: item.Flags, Judge: item.Judge}
}

// sortClosureRecords 排序: 先排"最该动手"的。
//
// 顺序 = 可销单 > 变坏 > 换了病 > 没变 > 判断不了; 同档按 qid。
// 让人一眼看到"现在能销哪些单"和"哪里被我弄坏了"。
func sortClosureRecords(records []ClosureRecord) {
	rank := func(verdict string) int {
		switch verdict {
		case ClosureImproved:
			return 0
		case ClosureWorsened:
			return 1
		case ClosureChanged:
			return 2
		case ClosureStable:
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		leftRank, rightRank := rank(records[i].Verdict), rank(records[j].Verdict)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return records[i].QID < records[j].QID
	})
}

// flagText 把标签列表拼成一句人话(空列表说"无标签")。
func flagText(flags []string) string {
	if len(flags) == 0 {
		return "无标签"
	}
	out := ""
	for index, flag := range flags {
		if index > 0 {
			out += " / "
		}
		out += flag
	}
	return out
}
