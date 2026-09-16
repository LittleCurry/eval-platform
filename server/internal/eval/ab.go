package eval

import (
	"math"
	"sort"
)

// A/B 对比的纯计算层(M5-1, process.md D6/D18)。
//
// 这里**不碰 DB、不碰 HTTP**: 输入是两次 run 的单题视图, 输出是一份可以直接渲染/导出的报告。
// 三条证据并排是刻意的设计(缺一不可):
//  1. 逐题差值(paired) —— 差了多少;
//  2. Wilcoxon 显著性   —— 这个差是不是碰运气(仅作参考, 见下);
//  3. 翻转题清单        —— 具体哪几道题变好/变坏(最稳、最有说服力的证据)。
//
// 为什么不把 p 值当结论: 检索指标是**离散且大量并列**的(recall 常取值只有 0/0.5/1.0,
// 60 题里 55 题都是满分), Wilcoxon 的正态近似在大量 ties 下并不可靠。所以结论以
// "跨 run 噪声底(M3 实测: 中位 3e-4 / 最大 2.1e-3) + 翻转题清单"为准, p 值只用于旁证。

// MetricSpec 一个可对比指标: 怎么取值, 以及"这题在该指标上可比吗"。
//
// 为什么不是一串名字: 生成侧指标的前提是**两侧都有判定**——"左边没判定"绝不能被当成
// "左边零幻觉"(M4-2 rubric v1 的老教训)。取值器返回 ok=false 就表示该题在该指标上
// 不可比, 会被剔除出配对样本, 并把实际参与对比的题数报给前端。
type MetricSpec struct {
	Name string
	// HigherIsBetter 指标方向: 幻觉率/无关率是**越低越好**, 报告与配色都要按方向判定,
	// 否则"幻觉率下降"会被记成"恶化"并涂成红色。零值按"越高越好"处理。
	HigherIsBetter bool
	// Value 返回该题在该指标上的值; ok=false 表示不可比(缺判定/缺 rubric)。
	Value func(ABCase) (float64, bool)
}

// RetrievalMetrics 检索侧指标(与 worker 侧 CaseMetric 字段名一致)。
func RetrievalMetrics() []MetricSpec {
	names := []string{"recall", "reciprocal_rank", "hit", "precision"}
	specs := make([]MetricSpec, 0, len(names))
	for _, name := range names {
		metric := name
		specs = append(specs, MetricSpec{
			Name:           metric,
			HigherIsBetter: true,
			Value: func(item ABCase) (float64, bool) {
				value, ok := item.Metrics[metric]
				return value, ok
			},
		})
	}
	return specs
}

// GenerationMetrics 生成侧指标(M5-1 尾巴): 全部要求该题**确实有判定**。
//
// 三个率的分母是 claims_total: 没有断言(答案只说"资料里未提及")时率值无意义,
// 因此这类题不进配对样本; helpfulness/relevance 要求该题打了 rubric。
func GenerationMetrics() []MetricSpec {
	// 三个率: 支持率越高越好, 幻觉率/无关率越低越好
	rate := func(name string, higherIsBetter bool, pick func(ABJudge) int) MetricSpec {
		return MetricSpec{
			Name:           name,
			HigherIsBetter: higherIsBetter,
			Value: func(item ABCase) (float64, bool) {
				if item.Judge == nil || item.Judge.Claims <= 0 {
					return 0, false
				}
				return float64(pick(*item.Judge)) / float64(item.Judge.Claims), true
			},
		}
	}
	score := func(name string, pick func(ABJudge) float64) MetricSpec {
		return MetricSpec{
			Name:           name,
			HigherIsBetter: true,
			Value: func(item ABCase) (float64, bool) {
				if item.Judge == nil || !item.Judge.HasRubric {
					return 0, false
				}
				return pick(*item.Judge), true
			},
		}
	}
	return []MetricSpec{
		rate("claim_support_rate", true, func(judge ABJudge) int { return judge.Supported }),
		rate("hallucination_rate", false, func(judge ABJudge) int { return judge.Unsupported }),
		rate("irrelevant_rate", false, func(judge ABJudge) int { return judge.Irrelevant }),
		score("helpfulness", func(judge ABJudge) float64 { return judge.Helpfulness }),
		score("relevance", func(judge ABJudge) float64 { return judge.Relevance }),
	}
}

// MetricsCompared 兼容旧名: 只跑检索侧时的指标名列表。
var MetricsCompared = []string{"recall", "reciprocal_rank", "hit", "precision"}

// DefaultNoiseFloor 跨 run 分数噪声底(绝对值), 取自 M3 故障演练实测的最大偏差。
const DefaultNoiseFloor = 2.1e-3

// MinSharedRatio 题集交集占比低于它就算"不可比"(避免拿半份题硬算平均值)。
const MinSharedRatio = 0.5

// ABOptions 对比参数。
type ABOptions struct {
	// NoiseFloor 差值绝对值小于它视为"落在噪声里", 不宣称变化。
	NoiseFloor float64
	// Metrics 参与对比的指标名(空则用 MetricsCompared)。
	Metrics []string
	// IncludeGeneration 是否把生成侧指标(三率 + rubric 均值)一并对比。
	IncludeGeneration bool
	// LeftAttribution / RightAttribution: 该 run 的 flags 是否已由归因规则写过
	// (即 runs.metrics.attribution 存在)。**只在一侧缺失时**才算问题 —— 见 ABReport.AttributionMissing。
	LeftAttribution  bool
	RightAttribution bool
}

// ABJudge 一道题判定结果的摘要(从 case_results.judge 抽出, 只留对比要用的计数与分数)。
type ABJudge struct {
	Claims      int
	Supported   int
	Unsupported int
	Irrelevant  int
	Relevance   float64
	Helpfulness float64
	HasRubric   bool
}

// ABCase 一次 run 里某道题参与对比的部分。
type ABCase struct {
	QID        string
	Category   string
	Difficulty string
	Metrics    map[string]float64
	Flags      []string
	// Judge 为 nil 表示这道题没有判定(只跑检索的 run, 或判定失败)。
	Judge *ABJudge
}

// MetricDelta 单个指标的 A/B 差值。
type MetricDelta struct {
	Left      float64 `json:"left"`
	Right     float64 `json:"right"`
	Delta     float64 `json:"delta"`
	Improved  int     `json:"improved"`
	Worsened  int     `json:"worsened"`
	Unchanged int     `json:"unchanged"`
	// Cases 实际参与该指标对比的题数: 生成侧只统计两侧都有判定的题,
	// 前端必须显示出来 —— 否则"幻觉率对比"基于 60 题还是 3 题没人知道。
	Cases int `json:"cases"`
	// HigherIsBetter 指标方向: false 时 delta 为负才是好事(幻觉率下降)。
	HigherIsBetter bool    `json:"higher_is_better"`
	PValue         float64 `json:"p_value"`
	Significant    bool    `json:"significant"`
	BelowNoise     bool    `json:"below_noise"`
}

// CaseDelta 一道题的标签变化(Fixed/Broke/Changed 用)。
type CaseDelta struct {
	QID        string   `json:"qid"`
	Category   string   `json:"category,omitempty"`
	Difficulty string   `json:"difficulty,omitempty"`
	FlagsLeft  []string `json:"flags_left"`
	FlagsRight []string `json:"flags_right"`
}

// Stratum 一个分组(按类别/难度/标签)的汇总。
type Stratum struct {
	Key          string  `json:"key"`
	Cases        int     `json:"cases"`
	MeanDelta    float64 `json:"mean_delta"`
	Improved     int     `json:"improved"`
	Worsened     int     `json:"worsened"`
	FlaggedLeft  int     `json:"flagged_left"`
	FlaggedRight int     `json:"flagged_right"`
}

// ABReport A/B 对比结果(直接作为 API 响应体)。
type ABReport struct {
	Left  int64 `json:"left"`
	Right int64 `json:"right"`

	// Comparable=false 时下面的统计没有意义, Reason 说明原因。
	Comparable bool   `json:"comparable"`
	Reason     string `json:"reason,omitempty"`

	LeftCases   int `json:"left_cases"`
	RightCases  int `json:"right_cases"`
	SharedCases int `json:"shared_cases"`

	Summary    map[string]MetricDelta `json:"summary"`
	NoiseFloor float64                `json:"noise_floor"`

	// Fixed: A 有问题 → B 干净; Broke: 反过来; Changed: 都有问题但标签变了(不算好也不算坏)。
	Fixed   []CaseDelta `json:"fixed"`
	Broke   []CaseDelta `json:"broke"`
	Changed []CaseDelta `json:"changed"`

	// JudgedCases 两侧都有判定的题数(生成侧指标的配对样本量)。
	JudgedCases int `json:"judged_cases"`
	// GenerationNote 非空表示生成侧指标这次没得比(例如两侧都没有判定)。
	GenerationNote string `json:"generation_note,omitempty"`

	// AttributionMissing 非空表示标签层面的对比不可信(两侧归因准备度不一致)。
	// 场景: A 做过归因(--apply)而 B 没做过 -> B 的 flags 是空的, 于是每一道有标签的题
	// 都会被算成"被修好了"。这种假阳性比不给结论更糟, 所以必须显式喊出来。
	AttributionMissing string `json:"attribution_missing,omitempty"`

	ByCategory   []Stratum `json:"by_category"`
	ByDifficulty []Stratum `json:"by_difficulty"`
	ByFlag       []Stratum `json:"by_flag"`
}

// ComputeAB 计算 A/B 报告。cases 的顺序不影响结果(按 qid 对齐)。
func ComputeAB(left, right []ABCase, opts ABOptions) ABReport {
	if opts.NoiseFloor <= 0 {
		opts.NoiseFloor = DefaultNoiseFloor
	}
	metrics := opts.Metrics
	if len(metrics) == 0 {
		metrics = MetricsCompared
	}

	report := ABReport{
		Comparable:         true,
		AttributionMissing: attributionMismatch(opts),
		LeftCases:          len(left),
		RightCases:         len(right),
		Summary:            map[string]MetricDelta{},
		NoiseFloor:         opts.NoiseFloor,
		Fixed:              []CaseDelta{},
		Broke:              []CaseDelta{},
		Changed:            []CaseDelta{},
		ByCategory:         []Stratum{},
		ByDifficulty:       []Stratum{},
		ByFlag:             []Stratum{},
	}

	rightByQID := make(map[string]ABCase, len(right))
	for _, item := range right {
		rightByQID[item.QID] = item
	}

	sharedLeft := make([]ABCase, 0, len(left))
	sharedRight := make([]ABCase, 0, len(left))
	for _, item := range left {
		counterpart, ok := rightByQID[item.QID]
		if !ok {
			continue
		}
		sharedLeft = append(sharedLeft, item)
		sharedRight = append(sharedRight, counterpart)
	}
	report.SharedCases = len(sharedLeft)

	if report.SharedCases == 0 {
		report.Comparable = false
		report.Reason = "两次 run 没有共同的题目(qid 无交集), 无法做配对比较"
		return report
	}
	widest := max(len(left), len(right))
	if float64(report.SharedCases) < MinSharedRatio*float64(widest) {
		report.Comparable = false
		report.Reason = "共同题目不足一半, 配对比较会失真(请确认两次 run 用的是同一评测集)"
		return report
	}

	specs := retrievalSpecs(metrics)
	if opts.IncludeGeneration {
		specs = append(specs, GenerationMetrics()...)
	}
	for _, spec := range specs {
		report.Summary[spec.Name] = compareMetric(sharedLeft, sharedRight, spec, opts.NoiseFloor)
	}
	if opts.IncludeGeneration {
		report.JudgedCases = judgedPairs(sharedLeft, sharedRight)
		if report.JudgedCases == 0 {
			report.GenerationNote = "两次 run 都没有可配对的判定结果(生成侧指标需要两侧都跑过 judge), " +
				"所以这几个指标这次没有对比 —— 注意: 「没有判定」不等于「没有幻觉」。"
		}
	}

	for index := range sharedLeft {
		leftCase, rightCase := sharedLeft[index], sharedRight[index]
		leftFlagged := len(leftCase.Flags) > 0
		rightFlagged := len(rightCase.Flags) > 0
		switch {
		case leftFlagged && !rightFlagged:
			report.Fixed = append(report.Fixed, caseDelta(leftCase, rightCase))
		case !leftFlagged && rightFlagged:
			report.Broke = append(report.Broke, caseDelta(leftCase, rightCase))
		case leftFlagged && rightFlagged && !sameFlags(leftCase.Flags, rightCase.Flags):
			report.Changed = append(report.Changed, caseDelta(leftCase, rightCase))
		}
	}

	report.ByCategory = stratify(sharedLeft, sharedRight, func(item ABCase) string { return item.Category })
	report.ByDifficulty = stratify(sharedLeft, sharedRight, func(item ABCase) string { return item.Difficulty })
	report.ByFlag = stratifyByFlag(sharedLeft, sharedRight)

	return report
}

// attributionMismatch 只在两侧准备度不一致时给警告(两侧都没做过时 flags 一律为空,
// Fixed/Broke 自然都是空的, 不会产生假阳性, 不必打扰)。
func attributionMismatch(opts ABOptions) string {
	switch {
	case opts.LeftAttribution && !opts.RightAttribution:
		return "右侧 run 未做过归因(缺少 runs.metrics.attribution), 它的归因标签是空的 —— " +
			"于是每道有标签的题都会被算成\"被修好\"。请先对该 run 执行 `make attribution RUN=<id> APPLY=1` 再对比。"
	case !opts.LeftAttribution && opts.RightAttribution:
		return "左侧 run 未做过归因(缺少 runs.metrics.attribution), 它的归因标签是空的 —— " +
			"于是每道有标签的题都会被算成\"变坏了\"。请先对该 run 执行 `make attribution RUN=<id> APPLY=1` 再对比。"
	default:
		return ""
	}
}

// retrievalSpecs 把指标名列表翻译成取值器; 名字不认识时跳过(旧调用方的兼容口)。
func retrievalSpecs(names []string) []MetricSpec {
	known := RetrievalMetrics()
	byName := make(map[string]MetricSpec, len(known))
	for _, spec := range known {
		byName[spec.Name] = spec
	}
	out := make([]MetricSpec, 0, len(names))
	for _, name := range names {
		if spec, ok := byName[name]; ok {
			out = append(out, spec)
		}
	}
	return out
}

// judgedPairs 统计两侧都有判定的题数(生成侧指标的配对样本量)。
func judgedPairs(left, right []ABCase) int {
	count := 0
	for index := range left {
		if left[index].Judge != nil && right[index].Judge != nil {
			count++
		}
	}
	return count
}

func caseDelta(left, right ABCase) CaseDelta {
	return CaseDelta{
		QID:        left.QID,
		Category:   left.Category,
		Difficulty: left.Difficulty,
		FlagsLeft:  append([]string{}, left.Flags...),
		FlagsRight: append([]string{}, right.Flags...),
	}
}

func sameFlags(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	sortedLeft := append([]string{}, left...)
	sortedRight := append([]string{}, right...)
	sort.Strings(sortedLeft)
	sort.Strings(sortedRight)
	for index := range sortedLeft {
		if sortedLeft[index] != sortedRight[index] {
			return false
		}
	}
	return true
}

// compareMetric 单个指标的配对比较: 均值差 + 改善/恶化题数 + Wilcoxon p + 噪声底判定。
//
// 只有**两侧都给出有效值**的题才进配对样本: 这是生成侧指标必须守的那条线
// (缺判定的题若按 0 参与, 就会把"没判定"读成"零幻觉")。
func compareMetric(left, right []ABCase, spec MetricSpec, noiseFloor float64) MetricDelta {
	delta := MetricDelta{}
	diffs := make([]float64, 0, len(left))
	for index := range left {
		leftValue, leftOK := spec.Value(left[index])
		rightValue, rightOK := spec.Value(right[index])
		if !leftOK || !rightOK {
			continue
		}
		diff := rightValue - leftValue
		diffs = append(diffs, diff)
		delta.Left += leftValue
		delta.Right += rightValue
		// 方向修正后再判定好坏: 幻觉率 -0.3 是改善, 不是恶化
		improvement := diff
		if !spec.HigherIsBetter {
			improvement = -diff
		}
		switch {
		// 判定"改善/恶化"的粒度是噪声底: 小于它只算"没变", 免得把抖动说成变化
		case improvement > noiseFloor:
			delta.Improved++
		case improvement < -noiseFloor:
			delta.Worsened++
		default:
			delta.Unchanged++
		}
	}
	count := float64(len(diffs))
	delta.Cases = len(diffs)
	if count > 0 {
		delta.Left = round(delta.Left/count, 6)
		delta.Right = round(delta.Right/count, 6)
	}
	delta.Delta = round(delta.Right-delta.Left, 6)
	delta.BelowNoise = math.Abs(delta.Delta) <= noiseFloor
	delta.HigherIsBetter = spec.HigherIsBetter

	_, pValue, ok := WilcoxonSignedRank(diffs)
	if ok {
		delta.PValue = round(pValue, 6)
		// 显著 = 统计显著 且 差值超出噪声底: 两个条件缺一不可
		delta.Significant = pValue < 0.05 && !delta.BelowNoise
	} else {
		delta.PValue = 1
	}
	return delta
}

// stratify 按某个维度分组统计: 组内均值差(recall) + 改善/恶化题数 + 有标签题数。
func stratify(left, right []ABCase, keyOf func(ABCase) string) []Stratum {
	buckets := map[string]*Stratum{}
	for index := range left {
		key := keyOf(left[index])
		if key == "" {
			key = "未分类"
		}
		bucket, ok := buckets[key]
		if !ok {
			bucket = &Stratum{Key: key}
			buckets[key] = bucket
		}
		bucket.Cases++
		diff := right[index].Metrics["recall"] - left[index].Metrics["recall"]
		bucket.MeanDelta += diff
		switch {
		case diff > DefaultNoiseFloor:
			bucket.Improved++
		case diff < -DefaultNoiseFloor:
			bucket.Worsened++
		}
		if len(left[index].Flags) > 0 {
			bucket.FlaggedLeft++
		}
		if len(right[index].Flags) > 0 {
			bucket.FlaggedRight++
		}
	}
	out := make([]Stratum, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.Cases > 0 {
			bucket.MeanDelta = round(bucket.MeanDelta/float64(bucket.Cases), 6)
		}
		out = append(out, *bucket)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MeanDelta != out[j].MeanDelta {
			return out[i].MeanDelta < out[j].MeanDelta // 最差的排前面
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// stratifyByFlag 按归因标签分组: "这个标签在 B 里还剩多少条"是 M4-3 之后最直观的 A/B 结论。
func stratifyByFlag(left, right []ABCase) []Stratum {
	keys := map[string]bool{}
	for _, item := range left {
		for _, flag := range item.Flags {
			keys[flag] = true
		}
	}
	for _, item := range right {
		for _, flag := range item.Flags {
			keys[flag] = true
		}
	}

	out := make([]Stratum, 0, len(keys))
	for key := range keys {
		bucket := Stratum{Key: key}
		for index := range left {
			hasLeft := contains(left[index].Flags, key)
			hasRight := contains(right[index].Flags, key)
			if hasLeft {
				bucket.FlaggedLeft++
			}
			if hasRight {
				bucket.FlaggedRight++
			}
			if hasLeft || hasRight {
				bucket.Cases++
				diff := right[index].Metrics["recall"] - left[index].Metrics["recall"]
				bucket.MeanDelta += diff
				switch {
				case diff > DefaultNoiseFloor:
					bucket.Improved++
				case diff < -DefaultNoiseFloor:
					bucket.Worsened++
				}
			}
		}
		if bucket.Cases > 0 {
			bucket.MeanDelta = round(bucket.MeanDelta/float64(bucket.Cases), 6)
		}
		out = append(out, bucket)
	}
	// 左多右少(修好了)的标签排前面
	sort.Slice(out, func(i, j int) bool {
		dropI := out[i].FlaggedLeft - out[i].FlaggedRight
		dropJ := out[j].FlaggedLeft - out[j].FlaggedRight
		if dropI != dropJ {
			return dropI > dropJ
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// WilcoxonSignedRank 配对符号秩检验(双侧), 返回 (W+, p)。
//
// - 差值为 0 的样本按惯例剔除(zero method: wilcox);
// - 秩按绝对值排, 并列取平均秩(ties correction);
// - 样本量小时用**精确分布**(DP 数秩和, 不枚举 2^n), 大样本用带连续性校正的正态近似;
// - 全部差值为 0 时无法检验, 返回 ok=false(调用方把 p 记 1, 不宣称显著)。
//
// 自研而非引 scipy: worker 运行时依赖刻意只有 5 个, 为一条 CLI 拖进 numpy+scipy 不划算;
// 公式短、可测(手算样例 + tests 里用 scipy 交叉验证, 缺库则 skip)。
func WilcoxonSignedRank(diffs []float64) (stat float64, pValue float64, ok bool) {
	nonZero := make([]float64, 0, len(diffs))
	for _, diff := range diffs {
		if diff != 0 {
			nonZero = append(nonZero, diff)
		}
	}
	n := len(nonZero)
	if n == 0 {
		return 0, 1, false
	}

	// 按绝对值排序并给平均秩
	type ranked struct {
		value float64
		rank  float64
	}
	items := make([]ranked, n)
	for index, value := range nonZero {
		items[index] = ranked{value: math.Abs(value)}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].value < items[j].value })
	tieGroups := make([][2]int, 0) // 每组: [起始下标, 长度]
	index := 0
	for index < n {
		end := index
		for end+1 < n && items[end+1].value == items[index].value {
			end++
		}
		length := end - index + 1
		// 平均秩: (index+1 + end+1) / 2
		average := float64(index+1+end+1) / 2
		for position := index; position <= end; position++ {
			items[position].rank = average
		}
		if length > 1 {
			tieGroups = append(tieGroups, [2]int{index, length})
		}
		index = end + 1
	}

	// W+ = 正差值的秩和(排序后按绝对值顺序重新对齐符号)
	byAbs := make([]float64, n)
	copy(byAbs, nonZero)
	sort.Slice(byAbs, func(i, j int) bool { return math.Abs(byAbs[i]) < math.Abs(byAbs[j]) })
	wPlus := 0.0
	for position := 0; position < n; position++ {
		if byAbs[position] > 0 {
			wPlus += items[position].rank
		}
	}

	// 精确分布: 秩和的可达性用 DP 计数(每个秩选或不选)
	if n <= 25 {
		counts := map[int]float64{0: 1}
		for _, item := range items {
			weight := int(math.Round(item.rank * 2)) // ×2 让平均秩(半数)也落在整数网格
			next := make(map[int]float64, len(counts)*2)
			for sum, ways := range counts {
				next[sum] += ways
				next[sum+weight] += ways
			}
			counts = next
		}
		total := math.Pow(2, float64(n))
		observed := int(math.Round(wPlus * 2))
		center := int(math.Round(float64(n*(n+1)) / 4 * 2))
		extreme := 0.0
		for sum, ways := range counts {
			if absInt(sum-center) >= absInt(observed-center) {
				extreme += ways
			}
		}
		return wPlus, math.Min(1, extreme/total), true
	}

	// 正态近似(带并列校正与连续性校正)
	mean := float64(n*(n+1)) / 4
	variance := float64(n*(n+1)*(2*n+1)) / 24
	tieCorrection := 0.0
	for _, group := range tieGroups {
		size := float64(group[1])
		tieCorrection += size*size*size - size
	}
	variance -= tieCorrection / 48
	if variance <= 0 {
		return wPlus, 1, true
	}
	z := (wPlus - mean)
	if z > 0 {
		z -= 0.5
	} else {
		z += 0.5
	}
	z /= math.Sqrt(variance)
	return wPlus, 2 * (1 - normalCDF(math.Abs(z))), true
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// normalCDF 标准正态分布函数(用 erf 的近似实现, 无需引依赖)。
func normalCDF(x float64) float64 {
	return 0.5 * (1 + math.Erf(x/math.Sqrt2))
}

func round(value float64, digits int) float64 {
	scale := math.Pow(10, float64(digits))
	return math.Round(value*scale) / scale
}
