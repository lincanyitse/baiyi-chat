// Package attach 按内容寻址保存附件。
//
// 为什么按内容寻址：同一张图被反复上传是常态（重发、多标签页），
// 按文件名存会产生多份副本。用内容哈希作 ID，天然去重，也让「这条
// 消息引用了哪个文件」变成不可变事实。
package attach

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store 保存附件二进制。
type Store struct {
	root string
}

// NewStore 构造存储。root 不存在时会在首次保存时创建。
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Info 是一个已保存附件的元信息。
type Info struct {
	ID        string
	MediaType string
	Size      int64
	// Name 是原始文件名，只用于展示，不参与寻址。
	Name string
}

// metaFile 是 .meta 伴生文件的内容。
type metaFile struct {
	MediaType string `json:"media_type"`
	Name      string `json:"name"`
}

// Save 保存内容并返回附件 ID（sha256 前 16 个十六进制字符）。
//
// 已存在相同内容时直接返回原 ID，不重复写盘。
func (s *Store) Save(data []byte, mediaType, name string) (string, error) {
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:8])

	path := s.pathOf(id)
	if _, err := os.Stat(path); err == nil {
		return id, nil
	}

	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return "", fmt.Errorf("创建附件目录失败: %w", err)
	}

	// 先写临时文件再改名：写到一半崩溃不会留下半截附件
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("写入附件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("保存附件失败: %w", err)
	}

	// 元信息单独存一个小文件，避免为「这是什么类型」再查一次数据库。
	// 用 JSON 而不是换行拼接：文件名是用户给的，含换行或 tab 都合法。
	meta, err := json.Marshal(metaFile{MediaType: mediaType, Name: name})
	if err != nil {
		return "", fmt.Errorf("编码附件元信息失败: %w", err)
	}
	if err := os.WriteFile(path+".meta", meta, 0o600); err != nil {
		return "", fmt.Errorf("写入附件元信息失败: %w", err)
	}
	return id, nil
}

// Load 读取附件内容与元信息。
func (s *Store) Load(id string) ([]byte, Info, error) {
	if !validID(id) {
		return nil, Info{}, fmt.Errorf("附件 ID 格式非法")
	}

	data, err := os.ReadFile(s.pathOf(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Info{}, fmt.Errorf("附件不存在")
		}
		return nil, Info{}, err
	}

	info := Info{ID: id, Size: int64(len(data))}
	if raw, err := os.ReadFile(s.pathOf(id) + ".meta"); err == nil {
		var m metaFile
		if json.Unmarshal(raw, &m) == nil {
			info.MediaType = m.MediaType
			info.Name = m.Name
		}
	}
	if info.MediaType == "" {
		info.MediaType = "application/octet-stream"
	}
	return data, info, nil
}

// Delete 删除附件。消息引用了它时不该调用——这里不检查引用。
func (s *Store) Delete(id string) error {
	if !validID(id) {
		return fmt.Errorf("附件 ID 格式非法")
	}
	_ = os.Remove(s.pathOf(id) + ".meta")
	return os.Remove(s.pathOf(id))
}

func (s *Store) pathOf(id string) string {
	return filepath.Join(s.root, id)
}

// validID 只允许十六进制，防止路径穿越。
func validID(id string) bool {
	if len(id) < 4 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !ok {
			return false
		}
	}
	return true
}
