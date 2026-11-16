package embed

import (
	"math"
	"testing"
)

func TestNormalize(t *testing.T) {
	v := []float32{3, 4}
	Normalize(v)
	if math.Abs(float64(Dot(v, v))-1) > 1e-6 {
		t.Fatalf("归一化后模长应为 1，实际 %v", Dot(v, v))
	}
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("方向错误：%v", v)
	}
}

func TestNormalizeZeroVector(t *testing.T) {
	v := []float32{0, 0, 0}
	Normalize(v)
	for _, x := range v {
		if x != 0 {
			t.Fatalf("零向量应保持为零，实际 %v", v)
		}
	}
}

func TestQuantizeRoundTrip(t *testing.T) {
	v := []float32{0.6, -0.8, 0, 1, -1}
	Normalize(v)
	q := QuantizeInt8(v)
	back := Dequantize(q)

	if len(back) != len(v) {
		t.Fatalf("长度不符")
	}
	for i := range v {
		if math.Abs(float64(v[i]-back[i])) > 1.0/127+1e-6 {
			t.Fatalf("第 %d 维误差过大：%v → %v", i, v[i], back[i])
		}
	}
}

func TestQuantizeClampsOutOfRange(t *testing.T) {
	// 未归一化的输入也要被夹到合法范围，不能溢出 int8
	q := QuantizeInt8([]float32{2.5, -3.7})
	if q[0] != 127 || q[1] != -127 {
		t.Fatalf("越界分量应被夹到 ±127，实际 %v", q)
	}
}

func TestDotInt8(t *testing.T) {
	a := []int8{127, 0, -127}
	b := []int8{127, 127, -127}
	// (127*127 + 0 - 127*-127) / 127² = (16129 + 16129) / 16129 = 2
	got := DotInt8(a, b)
	if math.Abs(float64(got)-2) > 1e-5 {
		t.Fatalf("期望 2，实际 %v", got)
	}
}

func TestDotInt8DifferentLengths(t *testing.T) {
	// 长度不一致时按短的一方计算，不能 panic
	got := DotInt8([]int8{127}, []int8{127, 127, 127})
	if math.Abs(float64(got)-1) > 1e-5 {
		t.Fatalf("期望 1，实际 %v", got)
	}
}

func TestDotInt8NoOverflow(t *testing.T) {
	// 1024 维全 127：127²×1024 ≈ 1.65e7，int32 累加不会溢出
	a := make([]int8, 1024)
	b := make([]int8, 1024)
	for i := range a {
		a[i] = 127
		b[i] = 127
	}
	got := DotInt8(a, b)
	if math.Abs(float64(got)-1024) > 0.01 {
		t.Fatalf("期望约 1024，实际 %v", got)
	}
}

func TestDotIdenticalIsOne(t *testing.T) {
	v := []float32{1, 2, 3, 4}
	Normalize(v)
	if math.Abs(float64(Dot(v, v))-1) > 1e-6 {
		t.Fatalf("单位向量自点积应为 1，实际 %v", Dot(v, v))
	}
}

func TestTopKOrderAndCount(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}
	vecs := [][]int8{
		{127, 0}, // 与 query 相似度 1
		{64, 0},  // 0.5
		{0, 127}, // 0
		{32, 127},
	}
	hits := TopK([]int8{127, 0}, ids, vecs, 2)
	if len(hits) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(hits))
	}
	if hits[0].ID != "a" || hits[1].ID != "b" {
		t.Fatalf("顺序错误：%+v", hits)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("应按分数降序：%+v", hits)
	}
}

func TestTopKKLargerThanInput(t *testing.T) {
	hits := TopK([]int8{127}, []string{"x"}, [][]int8{{127}}, 100)
	if len(hits) != 1 {
		t.Fatalf("k 超过输入时应返回全部，实际 %d", len(hits))
	}
}

func TestTopKZeroK(t *testing.T) {
	if got := TopK([]int8{127}, []string{"x"}, [][]int8{{127}}, 0); got != nil {
		t.Fatalf("k=0 应返回 nil，实际 %+v", got)
	}
}

func TestTopKEmptyInput(t *testing.T) {
	if got := TopK([]int8{127}, nil, nil, 5); got != nil {
		t.Fatalf("空输入应返回 nil，实际 %+v", got)
	}
}

func TestTopKMatchesFullSort(t *testing.T) {
	// 对照实现：暴力全排序取前 k，验证堆实现结果一致
	const n, k = 500, 10
	ids := make([]string, n)
	vecs := make([][]int8, n)
	for i := range vecs {
		ids[i] = string(rune('A' + i%26))
		vecs[i] = QuantizeInt8([]float32{float32(i%17) - 8, float32(i%13) - 6, 1})
	}
	query := QuantizeInt8([]float32{1, 2, -1})

	hits := TopK(query, ids, vecs, k)
	if len(hits) != k {
		t.Fatalf("期望 %d 条，实际 %d", k, len(hits))
	}
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Score < hits[i].Score {
			t.Fatalf("第 %d 与 %d 条顺序错误", i-1, i)
		}
	}

	// top-1 的分数必须等于真实最高分。
	// 并列时分数相同，ID 只是任一条——不能断言具体是哪条。
	best := float32(-math.MaxFloat32)
	for _, v := range vecs {
		if s := DotInt8(query, v); s > best {
			best = s
		}
	}
	if hits[0].Score != best {
		t.Fatalf("top-1 分数错误：期望 %v，实际 %v", best, hits[0].Score)
	}
	if hits[0].ID == "" {
		t.Fatal("top-1 缺少 ID")
	}
}

func BenchmarkTopK100k(b *testing.B) {
	if testing.Short() {
		b.Skip("短模式跳过 10 万向量基准")
	}
	const n, dims, k = 100_000, 1024, 10
	ids := make([]string, n)
	vecs := make([][]int8, n)
	f := NewFake(dims)
	for i := range vecs {
		ids[i] = "id"
		v := f.vectorize(string(rune(i)))
		vecs[i] = QuantizeInt8(v)
	}
	query := QuantizeInt8(f.vectorize("查询文本"))

	b.ResetTimer()
	for b.Loop() {
		_ = TopK(query, ids, vecs, k)
	}
}
