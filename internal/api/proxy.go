package api

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// loopback is trusted as a proxy when none are configured, which covers a
// reverse proxy on the same machine.
var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}

// ParseProxies parses trusted proxy addresses: IPs or CIDR ranges.
func ParseProxies(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is not an IP address or CIDR range", s)
		}
		ip = ip.Unmap()
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}

func (a *API) trusted(ip netip.Addr) bool {
	proxies := a.cfg.TrustedProxies
	if len(proxies) == 0 {
		proxies = loopback
	}
	ip = ip.Unmap()
	for _, p := range proxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP is the address a request came from. When it came through a
// trusted proxy, that is the nearest address in X-Forwarded-For that is
// not itself a trusted proxy; entries further left could be made up by
// the client.
func (a *API) clientIP(r *http.Request) string {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	ip := remote.Addr().Unmap()
	if !a.trusted(ip) {
		return ip.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		ip = hop.Unmap()
		if !a.trusted(ip) {
			break
		}
	}
	return ip.String()
}
