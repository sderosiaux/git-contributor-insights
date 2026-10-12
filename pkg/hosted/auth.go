package hosted

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type oauthState struct {
	Verifier string
	Next     string
	Expires  time.Time
}
type userKey struct{}

func (s *Server) isAdmin(login string) bool {
	for _, a := range append(append([]string{}, s.cfg.Admins...), s.store.admins()...) {
		if strings.EqualFold(login, a) {
			return true
		}
	}
	return false
}
func safeNext(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.ContainsAny(v, "\\\r\n") {
		return "/repositories"
	}
	return v
}
func (s *Server) cookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: !s.cfg.Development, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds())})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Development {
		http.Redirect(w, r, "/repositories", 303)
		return
	}
	state := randomToken()
	verifier := randomToken()
	challenge := sha256.Sum256([]byte(verifier))
	s.oauthMu.Lock()
	for k, v := range s.oauth {
		if time.Now().After(v.Expires) {
			delete(s.oauth, k)
		}
	}
	if len(s.oauth) >= 1000 {
		s.oauthMu.Unlock()
		http.Error(w, "Please try again shortly", 429)
		return
	}
	s.oauth[state] = oauthState{Verifier: verifier, Next: safeNext(r.URL.Query().Get("next")), Expires: time.Now().Add(10 * time.Minute)}
	s.oauthMu.Unlock()
	s.cookie(w, "atlas_oauth", state, 10*time.Minute)
	q := url.Values{"client_id": {s.cfg.ClientID}, "redirect_uri": {s.cfg.PublicURL + "/auth/callback"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	http.Redirect(w, r, s.cfg.GitHubWeb+"/login/oauth/authorize?"+q.Encode(), 303)
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie("atlas_oauth")
	if err != nil || len(state) != 64 || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		http.Error(w, "Invalid login state. Sign in again.", 400)
		return
	}
	s.oauthMu.Lock()
	pending, ok := s.oauth[state]
	delete(s.oauth, state)
	s.oauthMu.Unlock()
	s.cookie(w, "atlas_oauth", "", -time.Second)
	if !ok || time.Now().After(pending.Expires) || r.URL.Query().Get("code") == "" {
		http.Error(w, "Login expired. Sign in again.", 400)
		return
	}
	form := url.Values{"client_id": {s.cfg.ClientID}, "client_secret": {s.cfg.ClientSecret}, "code": {r.URL.Query().Get("code")}, "code_verifier": {pending.Verifier}, "redirect_uri": {s.cfg.PublicURL + "/auth/callback"}}
	req, err := http.NewRequestWithContext(r.Context(), "POST", s.cfg.GitHubWeb+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, "Unable to sign in", 500)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.github.client.Do(req)
	if err != nil {
		http.Error(w, "GitHub login is unavailable", 502)
		return
	}
	defer resp.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		Expires     int    `json:"expires_in"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token) != nil || token.AccessToken == "" {
		http.Error(w, "GitHub did not authorize this login", 403)
		return
	}
	login, err := s.github.identity(r.Context(), token.AccessToken)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	ttl := sessionLifetime
	if token.Expires > 0 && time.Duration(token.Expires)*time.Second < ttl {
		ttl = time.Duration(token.Expires) * time.Second
	}
	id := randomToken()
	if err = s.store.saveSession(id, session{login, token.AccessToken, time.Now().Add(ttl), time.Now()}); err != nil {
		http.Error(w, "Unable to save session", 500)
		return
	}
	s.cookie(w, "atlas_session", id, ttl)
	http.Redirect(w, r, pending.Next, 303)
}
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Development {
			login := "developer"
			if len(s.cfg.Admins) > 0 {
				login = s.cfg.Admins[0]
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, login)))
			return
		}
		cookie, err := r.Cookie("atlas_session")
		if err == nil {
			v, e := s.store.session(cookie.Value)
			if e == nil {
				if time.Since(v.Checked) > 10*time.Minute {
					login, e := s.github.identity(r.Context(), v.Token)
					if e != nil || !strings.EqualFold(login, v.Login) {
						s.store.logout(cookie.Value)
						s.unauthorized(w, r)
						return
					}
					v.Checked = time.Now()
					if e = s.store.refreshSession(cookie.Value, v); e != nil {
						http.Error(w, "Session unavailable", 503)
						return
					}
				}
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, v.Login)))
				return
			}
		}
		s.unauthorized(w, r)
	})
}
func (s *Server) unauthorized(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "/api/") {
		jsonError(w, 401, "Sign in to continue")
		return
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), 303)
}
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	login, _ := r.Context().Value(userKey{}).(string)
	if !s.isAdmin(login) {
		jsonError(w, 403, "Only workspace administrators can change repositories or settings")
		return false
	}
	return true
}
func (s *Server) checkMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != s.cfg.PublicURL || r.Header.Get("Sec-Fetch-Site") == "cross-site" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		jsonError(w, 403, "Make changes from this workspace")
		return false
	}
	return true
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func jsonError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("invalid request body")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("invalid request body")
	}
	return nil
}
