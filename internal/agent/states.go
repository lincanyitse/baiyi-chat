package agent

// state 是 Agent 循环的状态。
//
// 用显式枚举而非函数链：状态只有 8 个、需要可测试的转移边界，
// 且续跑与快照都要能挂钩到「当前在第几步」。
type state int

const (
	stIngest state = iota
	stAssemble
	stInfer
	stDecide
	stExecute
	stRefill
	stFinalize
	stDone
)

func (s state) String() string {
	switch s {
	case stIngest:
		return "ingest"
	case stAssemble:
		return "assemble"
	case stInfer:
		return "infer"
	case stDecide:
		return "decide"
	case stExecute:
		return "execute"
	case stRefill:
		return "refill"
	case stFinalize:
		return "finalize"
	case stDone:
		return "done"
	default:
		return "unknown"
	}
}

// transition 描述一次合法转移，用于校验与测试。
var transitions = map[state][]state{
	stIngest:   {stAssemble},
	stAssemble: {stInfer},
	stInfer:    {stDecide},
	stDecide:   {stExecute, stFinalize},
	stExecute:  {stRefill},
	stRefill:   {stInfer},
	stFinalize: {stDone},
	stDone:     {},
}

// canTransition 报告从 from 到 to 是否合法。
func canTransition(from, to state) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}
