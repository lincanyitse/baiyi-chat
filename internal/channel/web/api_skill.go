package web

import (
	"net/http"
)

// SkillDTO 是技能列表项。
//
// 只读：技能是文件，编辑它应该用文件管理器。这里只报告「有什么」与
// 「开没开」，外加足够的信息让用户认出是哪一个。
type SkillDTO struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Triggers    []string `json:"triggers"`
	Requires    []string `json:"requires"`
	// Enabled 是用户可切换的状态。与 Disabled 相反，但前端更愿意读正向语义。
	Enabled bool `json:"enabled"`
	// Dir 是技能目录的绝对路径，供用户去文件管理器里查看或修改。
	Dir string `json:"dir"`
	// References 是 references/ 下的文件数，让用户知道这个技能有多少展开内容。
	References int `json:"references"`
}

// handleListSkills 返回全部技能与启停状态。
func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	if s.opts.Skills == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "技能未配置")
		return
	}

	all := s.opts.Skills.AllForAdmin()
	out := make([]SkillDTO, 0, len(all))
	for _, sk := range all {
		out = append(out, SkillDTO{
			Name:        sk.Spec.Name,
			Description: sk.Spec.Description,
			Version:     sk.Spec.Version,
			Triggers:    sk.Spec.Triggers,
			Requires:    sk.Spec.Requires,
			Enabled:     s.opts.Skills.IsEnabled(sk.Spec.Name),
			Dir:         sk.Dir,
			References:  len(sk.References),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"skills": out,
		"root":   s.opts.Skills.Root(),
	})
}

// handleSetSkillEnabled 启用或禁用一项技能。
func (s *Server) handleSetSkillEnabled(w http.ResponseWriter, r *http.Request) {
	if s.opts.Skills == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "技能未配置")
		return
	}

	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	name := r.PathValue("name")
	if err := s.opts.Skills.SetEnabled(name, in.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}

	// 技能目录是上下文的稳定前缀段，启停后必须刷新——
	// 否则界面显示「已禁用」但模型这一轮仍看得到它。
	if s.opts.ReloadSkills != nil {
		if err := s.opts.ReloadSkills(); err != nil {
			// 状态已落盘，只是内存快照没刷上。返回错误让用户重试，
			// 而不是假装成功。
			writeError(w, http.StatusInternalServerError, ErrCodeInternal,
				"状态已保存，但刷新上下文失败: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleReloadSkills 强制重新扫描技能目录。
//
// 用户往目录里放了新技能后，不必重启进程。
func (s *Server) handleReloadSkills(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReloadSkills == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "技能未配置")
		return
	}
	if err := s.opts.ReloadSkills(); err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}
	s.handleListSkills(w, r)
}
