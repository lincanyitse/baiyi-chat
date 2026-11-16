package web

import (
	"errors"
	"net/http"
	"strings"
)

// sessionCookie 是登录凭证的 Cookie 名。
const sessionCookie = "baiyi_session"

// AuthStatusDTO 是认证状态，供前端决定是否显示登录页。
type AuthStatusDTO struct {
	// Enabled 为假表示没设口令，直接可用。
	Enabled bool `json:"enabled"`
	// LoggedIn 为真时当前请求已通过认证。
	LoggedIn bool `json:"logged_in"`
}

// authOrNotFound 取出 Auth；未启用时写好 404 并返回 nil。
//
// 四个写接口都要这段判断，重复四遍只会让新接口漏掉它。
func (s *Server) authOrNotFound(w http.ResponseWriter) *Auth {
	if s.opts.Auth == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "未启用口令")
		return nil
	}
	return s.opts.Auth
}

// handleAuthStatus 返回认证状态。这个接口不需要登录。
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Auth == nil {
		writeJSON(w, http.StatusOK, AuthStatusDTO{Enabled: false, LoggedIn: true})
		return
	}

	enabled, err := s.opts.Auth.Enabled()
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, AuthStatusDTO{
		Enabled: enabled,
		// 未启用口令时视为已登录，前端不必走登录流程
		LoggedIn: !enabled || s.opts.Auth.Valid(s.tokenFrom(r)),
	})
}

// handleLogin 校验口令并下发 Cookie。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	auth := s.authOrNotFound(w)
	if auth == nil {
		return
	}

	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	token, err := auth.Login(in.Password)
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	s.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, AuthStatusDTO{Enabled: true, LoggedIn: true})
}

// handleLogout 清除 Cookie 并作废 token。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.opts.Auth != nil {
		s.opts.Auth.Logout(s.tokenFrom(r))
	}
	s.setSessionCookie(w, r, "")
	writeJSON(w, http.StatusOK, AuthStatusDTO{Enabled: true, LoggedIn: false})
}

// handleSetupPassword 首次设置口令并直接登录。
func (s *Server) handleSetupPassword(w http.ResponseWriter, r *http.Request) {
	auth := s.authOrNotFound(w)
	if auth == nil {
		return
	}

	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	// Setup 自己签发 token：刚哈希过这个口令，不必再验证一遍
	token, err := auth.Setup(in.Password)
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	s.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, AuthStatusDTO{Enabled: true, LoggedIn: true})
}

// handleChangePassword 修改口令（需要旧口令）。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	auth := s.authOrNotFound(w)
	if auth == nil {
		return
	}

	var in struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	token, err := auth.Change(in.Old, in.New)
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	s.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, AuthStatusDTO{Enabled: true, LoggedIn: true})
}

// writeAuthErr 把认证错误映射成状态码。
func writeAuthErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoPassword):
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "尚未设置口令")
	case errors.Is(err, ErrBadPassword):
		// 不区分「用户不存在」与「口令错误」，也不透露任何细节
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "口令错误")
	default:
		// 参数问题（口令太短、重复设置）用 400；其余交给通用分支
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
	}
}

// tokenFrom 从 Cookie 或 Authorization 头取会话令牌。
//
// 同时支持 Bearer：curl/脚本用 Header 比管 Cookie 方便，
// 而浏览器用 Cookie（EventSource 无法自定义头）。
func (s *Server) tokenFrom(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// setSessionCookie 写入或清除会话 Cookie。
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	if token == "" {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		return
	}

	// Secure 只在 HTTPS 下需要；本地 HTTP 设了会导致浏览器不发送 Cookie。
	// 这里按请求是否 TLS 决定，兼容将来放在反代后面。
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// requireAuth 是认证中间件。
//
// 白名单：/api/health、/api/auth/*、静态资源。其余一律要求登录。
// 未启用口令时直接放行——这样单人本机使用时不会多一道无意义的门。
//
// 「是否启用」必须在**每次请求时**重新判断，不能在构造中间件时判断：
// 口令是运行期通过 /api/auth/setup 设置的，服务启动那一刻还没有，
// 提前判定会把中间件变成永久的空壳。Auth.Enabled 内部有缓存，
// 因此这里的每请求调用不是一次查库。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.opts.Auth == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enabled, err := s.opts.Auth.Enabled()
		if err != nil || !enabled {
			next.ServeHTTP(w, r)
			return
		}

		path := r.URL.Path
		// 前端要拿到登录状态才能决定显示什么，这个接口必须放行
		if path == "/api/health" || strings.HasPrefix(path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(path, "/api/") {
			// 静态资源放行：登录页本身需要 JS/CSS 才能渲染
			next.ServeHTTP(w, r)
			return
		}

		if !s.opts.Auth.Valid(s.tokenFrom(r)) {
			writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录")
			return
		}
		next.ServeHTTP(w, r)
	})
}
