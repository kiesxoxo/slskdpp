package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// bearer does a request with an Authorization header and no cookies.
func (e *env) bearer(t *testing.T, method, path, token string, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestV0SessionLogin(t *testing.T) {
	e := newEnv(t)

	if code, b := e.bearer(t, "GET", "/api/v0/session/enabled", "", ""); code != 200 || strings.TrimSpace(string(b)) != "true" {
		t.Errorf("enabled: %d %s", code, b)
	}
	if code, _ := e.bearer(t, "GET", "/api/v0/session", "", ""); code != 401 {
		t.Errorf("check without a token: %d", code)
	}

	// Blank fields are a 400 and do not count towards the lockout.
	for range 6 {
		if code, _ := e.bearer(t, "POST", "/api/v0/session", "", `{"username":"admin","password":""}`); code != 400 {
			t.Fatalf("blank password: %d", code)
		}
	}
	if code, _ := e.bearer(t, "POST", "/api/v0/session", "", ""); code != 400 {
		t.Errorf("no body: %d", code)
	}
	if code, _ := e.bearer(t, "POST", "/api/v0/session", "", `{"username":"admin","password":"nope"}`); code != 401 {
		t.Errorf("wrong password: %d", code)
	}

	before := time.Now().Unix()
	code, b := e.bearer(t, "POST", "/api/v0/session", "", `{"username":"admin","password":"secret"}`)
	var tok struct {
		Token, TokenType, Name     string
		Expires, Issued, NotBefore int64
	}
	json.Unmarshal(b, &tok)
	if code != 200 || tok.TokenType != "Bearer" || tok.Name != "admin" || tok.Issued < before ||
		tok.NotBefore != tok.Issued || tok.Expires-tok.Issued != int64(jwtTTL/time.Second) || strings.Count(tok.Token, ".") != 2 {
		t.Fatalf("login: %d %s", code, b)
	}
	// A standard JWT: HS256, with slskd's claims.
	parts := strings.Split(tok.Token, ".")
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	json.Unmarshal(payload, &claims)
	if string(header) != `{"alg":"HS256","typ":"JWT"}` || claims["name"] != "admin" || claims["role"] != "Administrator" || claims["iss"] != "slskd" {
		t.Errorf("token %s %s", header, payload)
	}

	// The token works on slskd's API and ours.
	for _, path := range []string{"/api/v0/session", "/api/v0/application", "/api/v1/downloads"} {
		if code, b := e.bearer(t, "GET", path, tok.Token, ""); code != 200 {
			t.Errorf("%s with the token: %d %s", path, code, b)
		}
	}
	// Tampered or truncated tokens do not.
	if code, b := e.bearer(t, "GET", "/api/v0/session", tok.Token[:len(tok.Token)-2]+"xx", ""); code != 401 || !strings.Contains(string(b), "token") {
		t.Errorf("tampered token: %d %s", code, b)
	}
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(payload), `"admin"`, `"root"`, 1))) + "." + parts[2]
	if code, _ := e.bearer(t, "GET", "/api/v0/session", forged, ""); code != 401 {
		t.Errorf("forged claims: %d", code)
	}
	// API keys still work as bearer tokens.
	if code, _ := e.bearer(t, "GET", "/api/v0/session", "config-key-123", ""); code != 200 {
		t.Errorf("API key as bearer: %d", code)
	}

	// Wrong passwords still lock the address out.
	for range maxLoginFailures {
		e.bearer(t, "POST", "/api/v0/session", "", `{"username":"admin","password":"nope"}`)
	}
	if code, _ := e.bearer(t, "POST", "/api/v0/session", "", `{"username":"admin","password":"secret"}`); code != 429 {
		t.Errorf("after repeated failures: %d", code)
	}
}

func TestV0JWTVerify(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	a := &API{cfg: Config{Username: "admin", SessionKey: []byte("0123456789abcdef0123456789abcdef")}, now: func() time.Time { return now }}
	valid := jwtClaims{Name: "admin", Role: "Administrator", NotBef: now.Unix(), IssuedAt: now.Unix(), Expires: now.Add(time.Hour).Unix()}
	if _, ok := a.verifyJWT(a.signJWT(valid)); !ok {
		t.Fatal("valid token refused")
	}
	for name, c := range map[string]jwtClaims{
		"expired":    {Name: "admin", NotBef: now.Add(-2 * time.Hour).Unix(), Expires: now.Add(-time.Hour).Unix()},
		"not yet":    {Name: "admin", NotBef: now.Add(time.Hour).Unix(), Expires: now.Add(2 * time.Hour).Unix()},
		"other user": {Name: "someone", NotBef: now.Unix(), Expires: now.Add(time.Hour).Unix()},
	} {
		if _, ok := a.verifyJWT(a.signJWT(c)); ok {
			t.Errorf("%s token accepted", name)
		}
	}
	// Unsigned ("alg":"none") tokens and session cookies are not tokens.
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body := strings.Split(a.signJWT(valid), ".")[1]
	if _, ok := a.verifyJWT(none + "." + body + "."); ok {
		t.Error("unsigned token accepted")
	}
	if _, ok := a.verifyJWT(a.signSession("admin", now.Add(time.Hour))); ok {
		t.Error("session cookie accepted as a token")
	}
	// Changing the web key invalidates tokens.
	b := &API{cfg: Config{Username: "admin", SessionKey: []byte("another key, another key, another")}, now: a.now}
	if _, ok := b.verifyJWT(a.signJWT(valid)); ok {
		t.Error("token from another key accepted")
	}
}
