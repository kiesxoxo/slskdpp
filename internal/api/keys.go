package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// keyPrefix starts every generated key so it is recognizable in configs
// and secret scanners.
const keyPrefix = "slskdpp_"

type apiKey struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Prefix   string    `json:"prefix"`
	Created  time.Time `json:"created,omitzero"`
	LastUsed time.Time `json:"last_used,omitzero"`
	// FromConfig keys are set in the config file and cannot be revoked here.
	FromConfig bool `json:"from_config,omitempty"`
}

// keyring checks API keys. Only hashes are kept.
type keyring struct {
	db     *sql.DB
	mu     sync.Mutex
	byHash map[string]int64 // hash -> key ID; config keys have negative IDs
	used   map[int64]time.Time
	config []apiKey
}

func hashKey(k string) string {
	sum := sha256.Sum256([]byte(k))
	return hex.EncodeToString(sum[:])
}

func newKeyring(db *sql.DB, configured []string) (*keyring, error) {
	k := &keyring{db: db, byHash: map[string]int64{}, used: map[int64]time.Time{}}
	for i, key := range configured {
		if key = strings.TrimSpace(key); key == "" {
			continue
		}
		id := -int64(i + 1)
		k.byHash[hashKey(key)] = id
		k.config = append(k.config, apiKey{ID: id, Name: "from config", Prefix: shortKey(key), FromConfig: true})
	}
	if db == nil {
		return k, nil
	}
	rows, err := db.Query(`SELECT id, hash FROM api_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		k.byHash[h] = id
	}
	return k, rows.Err()
}

func shortKey(k string) string {
	if len(k) > len(keyPrefix)+6 {
		return k[:len(keyPrefix)+6]
	}
	return k[:min(len(k), 4)]
}

// check reports whether key is valid, noting when it was last used.
func (k *keyring) check(key string) bool {
	h := hashKey(key)
	k.mu.Lock()
	id, ok := k.byHash[h]
	now := time.Now()
	write := ok && id > 0 && now.Sub(k.used[id]) > time.Minute
	if write {
		k.used[id] = now
	}
	k.mu.Unlock()
	if write {
		k.db.Exec(`UPDATE api_keys SET last_used = ? WHERE id = ?`, now.UnixMilli(), id)
	}
	return ok
}

func (k *keyring) create(name string) (apiKey, string, error) {
	b := make([]byte, 32)
	rand.Read(b)
	secret := keyPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	now := time.Now()
	res, err := k.db.Exec(`INSERT INTO api_keys (name, hash, prefix, created) VALUES (?, ?, ?, ?)`,
		name, hashKey(secret), shortKey(secret), now.UnixMilli())
	if err != nil {
		return apiKey{}, "", err
	}
	id, _ := res.LastInsertId()
	k.mu.Lock()
	k.byHash[hashKey(secret)] = id
	k.mu.Unlock()
	return apiKey{ID: id, Name: name, Prefix: shortKey(secret), Created: now}, secret, nil
}

func (k *keyring) list() ([]apiKey, error) {
	out := append([]apiKey{}, k.config...)
	rows, err := k.db.Query(`SELECT id, name, prefix, created, last_used FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key apiKey
		var created, used int64
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &created, &used); err != nil {
			return nil, err
		}
		key.Created = time.UnixMilli(created)
		if used > 0 {
			key.LastUsed = time.UnixMilli(used)
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (k *keyring) revoke(id int64) (bool, error) {
	res, err := k.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	k.mu.Lock()
	for h, kid := range k.byHash {
		if kid == id {
			delete(k.byHash, h)
		}
	}
	k.mu.Unlock()
	return true, nil
}

// requestKey returns the API key a request carries, if any.
func requestKey(r *http.Request) string {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	if v := r.Header.Get("Authorization"); len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

type ctxKey struct{}

// viaKey reports whether the request was authorized by an API key.
func viaKey(r *http.Request) bool { return r.Context().Value(ctxKey{}) != nil }

func withKey(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, true))
}

// sessionOnly keeps API keys away from endpoints that manage API keys.
func (a *API) sessionOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if viaKey(r) {
			writeError(w, http.StatusForbidden, "API keys cannot manage API keys; log in instead")
			return
		}
		next(w, r)
	}
}

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.keys.list()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, keys)
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name = strings.TrimSpace(req.Name); req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	key, secret, err := a.keys.create(req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, struct {
		apiKey
		Key string `json:"key"` // shown this once only
	}{key, secret})
}

func (a *API) revokeKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "no such key")
		return
	}
	switch ok, err := a.keys.revoke(id); {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	case !ok:
		writeError(w, http.StatusNotFound, "no such key")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
