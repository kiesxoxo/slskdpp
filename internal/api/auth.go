package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "slskdpp_session"
	sessionTTL    = 30 * 24 * time.Hour

	maxLoginFailures = 5
	loginWindow      = 5 * time.Minute
)

// LoadOrCreateKey reads the session signing key at path, creating a new
// random one if the file does not exist. Keeping it on disk keeps users
// logged in across restarts.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil && len(key) >= 32 {
		return key, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	rand.Read(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// RandomPassword returns a password for when none is configured.
func RandomPassword() string {
	b := make([]byte, 12)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// A session token is base64(username|expiry) "." base64(hmac).

func (a *API) signSession(username string, expires time.Time) string {
	payload := username + "|" + strconv.FormatInt(expires.Unix(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(a.mac(payload))
}

func (a *API) mac(payload string) []byte {
	m := hmac.New(sha256.New, a.cfg.SessionKey)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// verifySession returns the username in a valid, unexpired token.
func (a *API) verifySession(token string) (string, bool) {
	p64, s64, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(p64)
	sig, err2 := base64.RawURLEncoding.DecodeString(s64)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, a.mac(string(payload))) {
		return "", false
	}
	user, exp, ok := strings.Cut(string(payload), "|")
	unix, err := strconv.ParseInt(exp, 10, 64)
	if !ok || err != nil || a.now().Unix() > unix || user != a.cfg.Username {
		return "", false
	}
	return user, true
}

// auth rejects requests without a valid API key or session. A request
// that carries a key is judged by the key alone.
func (a *API) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A token from slskd's login stands in for the session cookie. It
		// is sent by hand, never by the browser, so it needs no cross-site
		// check. Anything else in the header is tried as an API key.
		if tok := bearerToken(r); tok != "" {
			if _, ok := a.verifyJWT(tok); ok {
				next(w, r)
				return
			}
			if strings.HasPrefix(tok, jwtHeader+".") {
				writeError(w, http.StatusUnauthorized, "token expired or invalid")
				return
			}
		}
		if key := requestKey(r); key != "" {
			if a.keys == nil || !a.keys.check(key) {
				writeError(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			next(w, withKey(r))
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if _, ok := a.verifySession(c.Value); !ok {
			writeError(w, http.StatusUnauthorized, "session expired or invalid")
			return
		}
		// The cookie is SameSite=Strict; refusing cross-site writes outright
		// also covers browsers that ignore that.
		if r.Method != http.MethodGet && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		next(w, r)
	}
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// checkLogin reads credentials and checks them against the web login,
// rate limiting failures by IP. It writes the error response itself. With
// rejectBlank, a blank username or password is a 400, as slskd answers,
// and does not count as a failure.
func (a *API) checkLogin(w http.ResponseWriter, r *http.Request, rejectBlank bool) (credentials, bool) {
	var c credentials
	ip := a.clientIP(r)
	if wait := a.limiter.blocked(ip, a.now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many failed logins, try again later")
		return c, false
	}
	if !readJSON(w, r, &c) {
		return c, false
	}
	if rejectBlank && (strings.TrimSpace(c.Username) == "" || strings.TrimSpace(c.Password) == "") {
		writeError(w, http.StatusBadRequest, "Username and/or Password missing or invalid")
		return c, false
	}
	userOK := subtle.ConstantTimeCompare([]byte(c.Username), []byte(a.cfg.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(c.Password), []byte(a.cfg.Password)) == 1
	if !userOK || !passOK || a.cfg.Password == "" {
		a.limiter.fail(ip, a.now())
		a.log.Warn("failed web login", "ip", ip, "username", c.Username)
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return c, false
	}
	a.limiter.reset(ip)
	return c, true
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	c, ok := a.checkLogin(w, r, false)
	if !ok {
		return
	}
	expires := a.now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.signSession(c.Username, expires),
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, r, http.StatusOK, map[string]string{"username": c.Username})
}

func (a *API) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]string{"username": a.cfg.Username})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// loginLimiter blocks an IP after maxLoginFailures failed logins within
// loginWindow.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string]*failures
}

type failures struct {
	count int
	start time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{fails: make(map[string]*failures)} }

// blocked returns how long ip must wait, or 0.
func (l *loginLimiter) blocked(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[ip]
	if f == nil || f.count < maxLoginFailures {
		return 0
	}
	if wait := f.start.Add(loginWindow).Sub(now); wait > 0 {
		return wait
	}
	delete(l.fails, ip)
	return 0
}

func (l *loginLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[ip]
	if f == nil || now.Sub(f.start) > loginWindow {
		if len(l.fails) > 10000 {
			for k, v := range l.fails {
				if now.Sub(v.start) > loginWindow {
					delete(l.fails, k)
				}
			}
		}
		f = &failures{start: now}
		l.fails[ip] = f
	}
	f.count++
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}
