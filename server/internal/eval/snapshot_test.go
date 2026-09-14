package eval

import (
	"os"
	"testing"
)

// 这两个期望值来自 worker(Python) 侧真实算出的指纹, 用于锁定"跨语言同算法"这一约定:
//   - chunking_hash: Python chunking_hash(ChunkingConfig()) 的输出
//   - config_hash:   Python snapshot_hash(build_config_snapshot(corpus=4, dataset=3, top_k=5))
//
// 若任一侧改动字段/序列化方式, 本测试立刻失败 —— 防止两侧"同配置"对不上。
const (
	wantChunkingHash = "5f45e03472e668aeb6b21965be6b23137522e50be45db9267646f4dce04fe16b"
	wantConfigHash   = "b6598ed95e12f5d919ac26a969173b7a067d070ea88b752ebff91d523b2b9d0b"
	// M4/D14: 启用生成后的新指纹(temperature=0)。仍由 Python 侧真实算出后回填,
	// 用于锁定"generation 段两侧同构" + "浮点归一化(0.0 -> 0)一致"。
	wantConfigHashWithGeneration = "e45129b63931df44bf17a1cdf04d475f157764aa4a4bafbc1e9d7edb1a36d31c"
	// 非整数温度也要对齐(证明浮点表示不是"只对了整数"的巧合)。
	wantConfigHashWithFractionalTemp = "ce02ebea0ba067f944f2fd4357d3aad5da16c6dd5fc09bd2777b779fdd76de60"
)

func generationSnapshot(temperature float64) SnapshotInput {
	generation := DefaultGeneration()
	generation.Temperature = temperature
	return SnapshotInput{
		Chunking:   DefaultChunking(),
		Embedding:  DefaultEmbedding(),
		CorpusID:   4,
		DatasetID:  3,
		TopK:       5,
		Generation: &generation,
	}
}

func TestChunkingHashMatchesWorker(t *testing.T) {
	got, err := ChunkingHash(DefaultChunking())
	if err != nil {
		t.Fatal(err)
	}
	if got != wantChunkingHash {
		t.Fatalf("chunking_hash 与 worker 侧不一致:\n got  = %s\n want = %s", got, wantChunkingHash)
	}
}

func TestSnapshotHashMatchesWorker(t *testing.T) {
	snapshot := BuildSnapshot(SnapshotInput{
		Chunking:  DefaultChunking(),
		Embedding: DefaultEmbedding(),
		CorpusID:  4,
		DatasetID: 3,
		TopK:      5,
	})
	got, err := SnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantConfigHash {
		t.Fatalf("config_hash 与 worker 侧不一致:\n got  = %s\n want = %s", got, wantConfigHash)
	}
}

// TestSnapshotWithoutGenerationKeepsLegacyHash 是 D14 的红线:
// 不启用生成时快照必须**字节级不变**, 否则 M2/M3 的全部历史 run 指纹作废。
func TestSnapshotWithoutGenerationKeepsLegacyHash(t *testing.T) {
	snapshot := BuildSnapshot(SnapshotInput{
		Chunking:  DefaultChunking(),
		Embedding: DefaultEmbedding(),
		CorpusID:  4,
		DatasetID: 3,
		TopK:      5,
	})
	if _, ok := snapshot["generation"]; ok {
		t.Fatal("未启用生成时不应写入 generation 键(D14)")
	}
}

func TestSnapshotHashWithGenerationMatchesWorker(t *testing.T) {
	cases := []struct {
		name        string
		temperature float64
		want        string
	}{
		{"整数温度", 0, wantConfigHashWithGeneration},
		{"小数温度", 0.3, wantConfigHashWithFractionalTemp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SnapshotHash(BuildSnapshot(generationSnapshot(tc.temperature)))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("带 generation 的 config_hash 与 worker 侧不一致:\n got  = %s\n want = %s", got, tc.want)
			}
		})
	}
}

func TestGenerationSectionShape(t *testing.T) {
	snapshot := BuildSnapshot(generationSnapshot(0))
	section, ok := snapshot["generation"].(map[string]any)
	if !ok {
		t.Fatalf("generation 段类型不符: %T", snapshot["generation"])
	}
	wantKeys := []string{"provider", "base_url", "model", "prompt_id", "temperature", "max_tokens", "max_context_chars"}
	for _, key := range wantKeys {
		if _, ok := section[key]; !ok {
			t.Fatalf("generation 段缺少字段 %s: %v", key, section)
		}
	}
	if _, isFloat := section["temperature"].(float64); isFloat {
		t.Fatalf("温度为整数时应归一化为整数(否则与 Python 的 JSON 表示不一致): %T", section["temperature"])
	}
}

func TestGenerationValidate(t *testing.T) {
	valid := DefaultGeneration()
	if err := valid.Validate(); err != nil {
		t.Fatalf("默认生成配置应当合法: %v", err)
	}
	bad := []struct {
		name   string
		mutate func(*GenerationConfig)
	}{
		{"缺少模型", func(g *GenerationConfig) { g.Model = "" }},
		{"缺少 prompt_id", func(g *GenerationConfig) { g.PromptID = "" }},
		{"温度越界", func(g *GenerationConfig) { g.Temperature = 3 }},
		{"max_tokens 非正", func(g *GenerationConfig) { g.MaxTokens = 0 }},
		{"上下文预算非正", func(g *GenerationConfig) { g.MaxContextChars = -1 }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultGeneration()
			tc.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatalf("非法配置应报错: %+v", config)
			}
		})
	}
}

func TestSnapshotIsKeyOrderIndependentAndSensitive(t *testing.T) {
	base := BuildSnapshot(SnapshotInput{
		Chunking: DefaultChunking(), Embedding: DefaultEmbedding(),
		CorpusID: 4, DatasetID: 3, TopK: 5,
	})
	baseHash, _ := SnapshotHash(base)

	reordered := map[string]any{}
	for _, key := range []string{"extras", "data", "retrieval", "embedding", "chunking"} {
		reordered[key] = base[key]
	}
	reorderedHash, _ := SnapshotHash(reordered)
	if baseHash != reorderedHash {
		t.Fatal("键序变化不应影响指纹")
	}

	changed := BuildSnapshot(SnapshotInput{
		Chunking: DefaultChunking(), Embedding: DefaultEmbedding(),
		CorpusID: 4, DatasetID: 3, TopK: 10,
	})
	changedHash, _ := SnapshotHash(changed)
	if changedHash == baseHash {
		t.Fatal("top_k 变化必须改变指纹")
	}
}

func TestCollectionNameMatchesWorkerConvention(t *testing.T) {
	hash, _ := ChunkingHash(DefaultChunking())
	if got := CollectionName(4, hash); got != "corpus4_5f45e034" {
		t.Fatalf("collection 命名不符: %s", got)
	}
}

func TestChunkingValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  ChunkingConfig
		ok   bool
	}{
		{"默认配置合法", DefaultChunking(), true},
		{"未知策略", ChunkingConfig{Strategy: "unknown", ChunkSize: 500, Overlap: 50}, false},
		{"chunk_size 为 0", ChunkingConfig{Strategy: "headings", ChunkSize: 0}, false},
		{"overlap 等于 chunk_size", ChunkingConfig{Strategy: "headings", ChunkSize: 100, Overlap: 100}, false},
		{"overlap 为负", ChunkingConfig{Strategy: "headings", ChunkSize: 100, Overlap: -1}, false},
		{"min_chars 为负", ChunkingConfig{Strategy: "headings", ChunkSize: 100, Overlap: 0, MinChars: -1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("应通过校验, 实际: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("应校验失败")
			}
		})
	}
}

func TestGitSHAEnvOverride(t *testing.T) {
	t.Setenv("EVAL_GIT_SHA", "testsha1")
	if got := GitSHA("/nonexistent"); got != "testsha1" {
		t.Fatalf("EVAL_GIT_SHA 未生效: %s", got)
	}
}

func TestGitSHAFallback(t *testing.T) {
	os.Unsetenv("EVAL_GIT_SHA")
	if got := GitSHA("/nonexistent-path-for-test"); got != "unknown" {
		t.Fatalf("非 git 目录应返回 unknown, 实际 %s", got)
	}
}
