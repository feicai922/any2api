package panel

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (h *Handler) adminHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		next := safeNextPath(r.URL.Query().Get("next"))
		if h.authorized(r) {
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
		h.renderLogin(w, http.StatusOK, next, "")
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
		if err := r.ParseForm(); err != nil {
			h.renderLogin(w, http.StatusRequestEntityTooLarge, "/", "登录请求无效或过大。")
			return
		}
		next := safeNextPath(r.FormValue("next"))
		now := time.Now()
		h.loginMu.Lock()
		if h.loginBlockedUntil.After(now) {
			remaining := time.Until(h.loginBlockedUntil).Round(time.Second)
			h.loginMu.Unlock()
			h.renderLogin(w, http.StatusTooManyRequests, next, "登录尝试过多，请在 "+remaining.String()+" 后重试。")
			return
		}
		if !h.loginBlockedUntil.IsZero() {
			h.loginBlockedUntil = time.Time{}
			h.loginFailures = 0
		}
		h.loginMu.Unlock()

		if !constantEqual(r.FormValue("password"), h.password) {
			blocked := h.recordLoginFailure(now)
			if blocked {
				h.renderLogin(w, http.StatusTooManyRequests, next, "登录尝试过多，已暂时锁定 30 秒。")
				return
			}
			h.renderLogin(w, http.StatusUnauthorized, next, "密码错误。")
			return
		}
		h.loginMu.Lock()
		h.loginFailures = 0
		h.loginBlockedUntil = time.Time{}
		h.loginMu.Unlock()
		h.createSession(w, r)
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) adminLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		h.sessionMu.Lock()
		delete(h.sessions, cookie.Value)
		h.sessionMu.Unlock()
	}
	h.clearSessionCookie(w, r)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *Handler) renderLogin(w http.ResponseWriter, status int, next, message string) {
	h.renderPanelPage(w, status, false, &loginPageData{Next: next, Error: message})
}

func (h *Handler) recordLoginFailure(now time.Time) bool {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	h.loginFailures++
	if h.loginFailures < 5 {
		return false
	}
	h.loginFailures = 0
	h.loginBlockedUntil = now.Add(30 * time.Second)
	return true
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "create session failed", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	sessionID := hex.EncodeToString(raw)
	h.sessionMu.Lock()
	for id, session := range h.sessions {
		if session.expires.Before(now) {
			delete(h.sessions, id)
		}
	}
	h.sessions[sessionID] = adminSession{expires: now.Add(adminSessionTTL)}
	h.sessionMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(adminSessionTTL / time.Second),
		Expires:  now.Add(adminSessionTTL),
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func (h *Handler) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	now := time.Now()
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	session, ok := h.sessions[cookie.Value]
	if !ok {
		return false
	}
	if session.expires.Before(now) {
		delete(h.sessions, cookie.Value)
		return false
	}
	return true
}

func (h *Handler) validToken(r *http.Request) bool {
	got := strings.TrimSpace(r.Header.Get("X-Panel-Token"))
	return got != "" && constantEqual(got, h.token)
}

func constantEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(proto, "https")
}

func safeNextPath(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\r\n\\") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
		return "/"
	}
	return next
}
