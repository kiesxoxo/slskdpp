package api

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	proxies, err := ParseProxies([]string{"10.0.0.0/8", " 192.168.1.5 "})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProxies([]string{"nonsense"}); err == nil {
		t.Fatal("accepted a bad proxy address")
	}
	withProxies := &API{cfg: Config{TrustedProxies: proxies}}
	defaults := &API{}
	for _, c := range []struct {
		api    *API
		remote string
		xff    []string
		want   string
	}{
		// Direct clients cannot pick their address.
		{withProxies, "203.0.113.9:4000", []string{"1.2.3.4"}, "203.0.113.9"},
		{withProxies, "10.1.2.3:4000", nil, "10.1.2.3"},
		{withProxies, "10.1.2.3:4000", []string{"198.51.100.7"}, "198.51.100.7"},
		// Only the nearest untrusted hop counts; the client can prepend
		// anything.
		{withProxies, "10.1.2.3:4000", []string{"6.6.6.6, 198.51.100.7, 192.168.1.5"}, "198.51.100.7"},
		{withProxies, "10.1.2.3:4000", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"},
		{withProxies, "10.1.2.3:4000", []string{"garbage, 198.51.100.7"}, "198.51.100.7"},
		{withProxies, "127.0.0.1:4000", []string{"198.51.100.7"}, "127.0.0.1"},
		// Without configuration only loopback is trusted.
		{defaults, "127.0.0.1:4000", []string{"198.51.100.7"}, "198.51.100.7"},
		{defaults, "[::1]:4000", []string{"2001:db8::1"}, "2001:db8::1"},
		{defaults, "10.1.2.3:4000", []string{"198.51.100.7"}, "10.1.2.3"},
	} {
		r := httptest.NewRequest("POST", "/api/v1/session", nil)
		r.RemoteAddr = c.remote
		for _, v := range c.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := c.api.clientIP(r); got != c.want {
			t.Errorf("remote %s, X-Forwarded-For %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
