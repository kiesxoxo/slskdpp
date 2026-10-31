package api

// slskd's login for API clients: POST /api/v0/session trades the web
// username and password for a JSON Web Token, sent back as
// "Authorization: Bearer <token>".

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// jwtTTL is how long a token lasts, slskd's default.
const jwtTTL = 7 * 24 * time.Hour

// jwtIssuer is the issuer slskd writes into its tokens.
const jwtIssuer = "slskd"

var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

type jwtClaims struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	ID       string `json:"jti"`
	Issuer   string `json:"iss"`
	IssuedAt int64  `json:"iat"`
	NotBef   int64  `json:"nbf"`
	Expires  int64  `json:"exp"`
}

// jwtKey signs tokens. It is derived from the session key, so tokens and
// session cookies cannot be passed off as each other.
func (a *API) jwtKey() []byte {
	m := hmac.New(sha256.New, a.cfg.SessionKey)
	m.Write([]byte("slskd api token"))
	return m.Sum(nil)
}

func (a *API) jwtSign(signed string) string {
	m := hmac.New(sha256.New, a.jwtKey())
	m.Write([]byte(signed))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *API) signJWT(c jwtClaims) string {
	payload, _ := json.Marshal(c)
	signed := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	return signed + "." + a.jwtSign(signed)
}

// verifyJWT returns the claims of a token we issued that is in force and
// names the current web user.
func (a *API) verifyJWT(tok string) (jwtClaims, bool) {
	var c jwtClaims
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] != jwtHeader {
		return c, false
	}
	if !hmac.Equal([]byte(parts[2]), []byte(a.jwtSign(parts[0]+"."+parts[1]))) {
		return c, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return c, false
	}
	now := a.now().Unix()
	if c.Name != a.cfg.Username || now >= c.Expires || now < c.NotBef {
		return c, false
	}
	return c, true
}

// bearerToken is the value of an "Authorization: Bearer" header.
func bearerToken(r *http.Request) string {
	if v := r.Header.Get("Authorization"); len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

// v0Login checks the web username and password and returns a token, as
// slskd's TokenResponse.
func (a *API) v0Login(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength == 0 {
		writeError(w, http.StatusBadRequest, "No credentials provided")
		return
	}
	c, ok := a.checkLogin(w, r, true)
	if !ok {
		return
	}
	now := a.now()
	claims := jwtClaims{
		Name: c.Username, Role: "Administrator", ID: newUUID(), Issuer: jwtIssuer,
		IssuedAt: now.Unix(), NotBef: now.Unix(), Expires: now.Add(jwtTTL).Unix(),
	}
	v0JSON(w, r, http.StatusOK, map[string]any{
		"expires":   claims.Expires,
		"issued":    claims.IssuedAt,
		"name":      claims.Name,
		"notBefore": claims.NotBef,
		"token":     a.signJWT(claims),
		"tokenType": "Bearer",
	})
}

// v0CheckSession answers 200 to any authenticated caller.
func (a *API) v0CheckSession(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// v0SessionEnabled tells clients that logging in is required; slskdpp
// always requires it.
func (a *API) v0SessionEnabled(w http.ResponseWriter, r *http.Request) {
	v0JSON(w, r, http.StatusOK, true)
}
