// Package httpguard holds request checks shared by the internal (operator
// and metrics) listeners.
package httpguard

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// HostAllowed reports whether r's Host is the listener's own host, a
// loopback address or localhost. Internal listeners refuse everything else:
// a web page that rebinds its DNS name to such a listener would otherwise
// talk to it as same-origin.
func HostAllowed(r *http.Request, listenHost string) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	ip, err := netip.ParseAddr(host)
	return strings.EqualFold(host, "localhost") || (err == nil && ip.IsLoopback()) ||
		(listenHost != "" && strings.EqualFold(host, strings.Trim(listenHost, "[]")))
}
