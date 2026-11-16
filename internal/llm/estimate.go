package llm

// EstimateTokens 是跨 Provider 的启发式 token 估算。
//
// 中文按 0.7 token/字，其他字符按 1 token/4 字符，图片粗略折算 800 token。
// 各 Provider 的 CountTokens 默认复用它；M1 接入精确估算时由
// contextx.Estimator 覆盖，这里保持为零依赖的兜底实现。
func EstimateTokens(req Request) int {
	total := 0
	if req.System != "" {
		total += EstimateText(req.System)
	}
	for _, m := range req.Messages {
		total += EstimateBlocks(m.Content)
		total += 4 // 每条消息的结构开销
	}
	return total
}

// EstimateBlocks 估算一组内容块的 token 数。
func EstimateBlocks(blocks []Block) int {
	total := 0
	for _, blk := range blocks {
		switch b := blk.(type) {
		case TextBlock:
			total += EstimateText(b.Text)
		case ThinkingBlock:
			total += EstimateText(b.Text)
		case ToolUseBlock:
			total += EstimateText(b.Name)
			total += EstimateText(string(b.Input))
		case ToolResultBlock:
			total += EstimateBlocks(b.Content)
		case CitationBlock:
			total += 8
		case ImageBlock:
			total += 800
		case AudioBlock:
			total += b.DurationMS * 20 // 粗略：50ms 约 1 token
		case FileBlock:
			total += EstimateText(b.Name)
		}
	}
	return total
}

// EstimateText 估算一段文本的 token 数。
func EstimateText(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk*7/10 + other/4 + 4
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK 统一汉字
		(r >= 0x3040 && r <= 0x30FF) || // 日文假名
		(r >= 0xAC00 && r <= 0xD7AF) // 韩文
}
