package session

// 分支树：消息用 parent_id 串成树，叶子节点决定当前视图。
//
// 为什么不原地改写：会话是用户的思考记录，重新生成或编辑一句话后
// 旧回答不该消失——它可能是被采纳过的。追加新节点、保留旧链，
// 用户随时能切回去。
//
// 数据结构（不改表，messages.parent_id 已在 0001 建好）：
//
//	user1 ← parent = NULL
//	  └ asst1a ← parent = user1
//	      └ user2 ← parent = asst1a
//	  └ asst1b ← parent = user1     ← 重新生成产生
//
// 默认 leaf = 会话里 seq 最大的消息；切分支时由前端传入指定 leaf。

import (
	"context"
	"fmt"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// BranchInfo 描述一条消息在分支树中的位置，供前端画「← 2/3 →」。
type BranchInfo struct {
	// Siblings 是同一父节点下的兄弟消息总数（含自己）。
	Siblings int
	// Index 是自己在兄弟中的序号，从 1 开始。
	Index int
}

// MessageNode 是消息树的节点。
type MessageNode struct {
	Message llm.Message
	// Branch 描述它在兄弟中的位置；只有存在多个兄弟时前端才显示切换器。
	Branch BranchInfo
	// Children 是它下面可分叉的子节点数（通常助手消息只有一个子节点，
	// 但用户在同一条回答后连发两条消息就会分叉）。
	Children int
}

// MessageTree 是一次会话消息的完整视图。
type MessageTree struct {
	// Leaf 是当前视图的叶子消息 ID；空表示会话没有消息。
	Leaf string
	// Chain 是从根到 Leaf 的消息链，按时间顺序。
	Chain []llm.Message
	// Nodes 是按 ID 索引的节点，含全部兄弟（不只是当前链上的）。
	Nodes map[string]MessageNode
}

// branchCacheKey 让缓存能区分同一会话的不同分支视图。
//
// 统一成一种形态（空 leaf 用 "@"）：失效时只需前缀扫描，
// 不必同时处理裸键与复合键。
func branchCacheKey(sessionID, leaf string) string {
	return sessionID + "@" + leaf
}

// History 返回会话当前分支的消息历史。
//
// leaf 为空时沿用会话里最新的一条消息作为叶子。这是 Web 与 REPL
// 的共同默认行为：打开会话看到的就是最后一次对话。
func (s *Service) History(ctx context.Context, sessionID string) ([]llm.Message, error) {
	return s.HistoryAt(ctx, sessionID, "")
}

// HistoryAt 返回指定叶子所在分支的消息链。
//
// 沿 parent_id 向上回溯到根再反转，拿到的是**一条完整的对话上下文**。
// 兄弟分支上的消息不会出现在这里——那正是分支的意义。
func (s *Service) HistoryAt(ctx context.Context, sessionID, leaf string) ([]llm.Message, error) {
	key := branchCacheKey(sessionID, leaf)

	// 缓存里存的是基准副本，每次返回都交出一份新切片：
	// 调用方（agent）会把它当作可自由读的上下文，不能与缓存共享底层数组。
	s.mu.Lock()
	cached, ok := s.history[key]
	s.mu.Unlock()
	if ok {
		return append([]llm.Message(nil), cached...), nil
	}

	stored, err := s.repo.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	chain := buildChain(stored, leaf)
	out := make([]llm.Message, 0, len(chain))
	for _, m := range chain {
		out = append(out, toLLMMessage(m))
	}

	s.mu.Lock()
	if _, exists := s.history[key]; !exists {
		keys := append(s.historyKeys[sessionID], key)
		// 超过上限就淘汰最早的那个视图
		for len(keys) > maxBranchCache {
			delete(s.history, keys[0])
			keys = keys[1:]
		}
		s.historyKeys[sessionID] = keys
	}
	s.history[key] = out
	s.mu.Unlock()

	return append([]llm.Message(nil), out...), nil
}

// MessageTreeOf 返回完整消息树，供 Web 画分支切换器。
func (s *Service) MessageTreeOf(ctx context.Context, sessionID, leaf string) (MessageTree, error) {
	stored, err := s.repo.ListMessages(ctx, sessionID)
	if err != nil {
		return MessageTree{}, err
	}

	tree := MessageTree{Nodes: make(map[string]MessageNode, len(stored))}
	children := childIndex(stored)

	for _, m := range stored {
		siblings := children[m.ParentID]
		info := BranchInfo{Siblings: len(siblings), Index: 1}
		for i, sib := range siblings {
			if sib == m.ID {
				info.Index = i + 1
				break
			}
		}
		tree.Nodes[m.ID] = MessageNode{
			Message:  toLLMMessage(m),
			Branch:   info,
			Children: len(children[m.ID]),
		}
	}

	chain := buildChain(stored, leaf)
	tree.Chain = make([]llm.Message, 0, len(chain))
	for _, m := range chain {
		tree.Chain = append(tree.Chain, toLLMMessage(m))
	}
	if len(chain) > 0 {
		tree.Leaf = chain[len(chain)-1].ID
	}
	return tree, nil
}

// toLLMMessage 把持久化消息转成上下文消息。
//
// 四个视图（历史、树、兄弟、分支选项）都要这一份转换，字段漏一处
// 就会出现「某个接口丢 ParentID」的隐性 bug。
func toLLMMessage(m StoredMessage) llm.Message {
	return llm.Message{
		ID:        m.ID,
		Role:      m.Role,
		Content:   m.Content,
		CreatedAt: m.CreatedAt,
		Meta:      llm.MessageMeta{ParentID: m.ParentID, Trust: m.Trust, Pinned: m.Pinned},
	}
}

// BranchOption 是一条可切换的分支：兄弟消息本身，以及切过去之后的视图末端。
type BranchOption struct {
	Message llm.Message
	// Leaf 是这条分支向下延伸的最深叶子，切换时作为 HistoryAt 的 leaf。
	Leaf string
}

// BranchOptions 返回某条消息所在位置的全部分支选项，一次查库算完。
//
// 为什么不让调用方自己拿兄弟列表再逐个求叶子：那是 1+N 次全量加载，
// 而这里只需要一次。分支切换器要的数据本来就属于「消息树」这个概念，
// 把它留在 session 层比散在渠道层更合适。
func (s *Service) BranchOptions(ctx context.Context, sessionID, messageID string) ([]BranchOption, error) {
	stored, err := s.repo.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]StoredMessage, len(stored))
	for _, m := range stored {
		byID[m.ID] = m
	}
	self, ok := byID[messageID]
	if !ok {
		return nil, fmt.Errorf("消息 %s 不存在: %w", messageID, ErrNotFound)
	}

	children := childIndex(stored)
	ids := children[self.ParentID]
	out := make([]BranchOption, 0, len(ids))
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			continue
		}
		out = append(out, BranchOption{
			Message: toLLMMessage(m),
			Leaf:    deepestLeaf(children, id),
		})
	}
	return out, nil
}

// deepestLeaf 沿「最新子节点」一路向下，返回最深叶子。
//
// 取 seq 最大的后代：同一父节点下后生成的回答通常更符合用户当前意图。
func deepestLeaf(children map[string][]string, from string) string {
	cur := from
	for {
		kids := children[cur]
		if len(kids) == 0 {
			return cur
		}
		cur = kids[len(kids)-1]
	}
}

// childIndex 建立 parent -> 子消息 ID 列表（按 seq 升序）。
//
// 用 seq 而不是 created_at：同一秒内写入的多条消息时间戳相同，
// seq 是唯一的严格次序。
func childIndex(stored []StoredMessage) map[string][]string {
	out := make(map[string][]string, len(stored))
	for _, m := range stored {
		out[m.ParentID] = append(out[m.ParentID], m.ID)
	}
	// ListMessages 已按 seq 升序，这里保持顺序即可
	return out
}

// buildChain 从 leaf 沿 parent_id 回溯到根并反转。
//
// parent_id 指向的节点可能不存在（历史数据损坏、或软删除的中间节点），
// 遇到断链就停在能走到的位置，不返回错误——用户至少能看到后半段。
func buildChain(stored []StoredMessage, leaf string) []StoredMessage {
	if len(stored) == 0 {
		return nil
	}

	byID := make(map[string]StoredMessage, len(stored))
	for _, m := range stored {
		byID[m.ID] = m
	}

	if leaf == "" {
		leaf = stored[len(stored)-1].ID
	}

	var rev []StoredMessage
	seen := make(map[string]bool, len(stored))
	for cur := leaf; cur != "" && !seen[cur]; {
		seen[cur] = true
		m, ok := byID[cur]
		if !ok {
			break
		}
		rev = append(rev, m)
		cur = m.ParentID
	}

	chain := make([]StoredMessage, len(rev))
	for i, m := range rev {
		chain[len(rev)-1-i] = m
	}
	return chain
}

// invalidateHistory 丢弃某会话的全部分支缓存。任何消息写入后必须调用。
func (s *Service) invalidateHistory(sessionID string) {
	prefix := sessionID + "@"
	s.mu.Lock()
	for key := range s.history {
		if strings.HasPrefix(key, prefix) {
			delete(s.history, key)
		}
	}
	delete(s.historyKeys, sessionID)
	s.mu.Unlock()
}
