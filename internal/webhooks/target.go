package webhooks

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// TargetPolicy decides which webhook URLs and addresses may be called.
// Unspecified, link-local (incl. the cloud metadata address), multicast and
// broadcast addresses are always refused; loopback and private networks
// only when AllowPrivate is set; plain http only when AllowHTTP is set.
type TargetPolicy struct {
	AllowHTTP    bool
	AllowPrivate bool
}

// ErrForbiddenTarget means a URL or address is not allowed by the policy.
var ErrForbiddenTarget = errors.New("webhook target not allowed")

// MaxURLLen bounds a webhook URL.
const MaxURLLen = 2048

const defaultClientTimeout = 10 * time.Second

func forbidden(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrForbiddenTarget, fmt.Sprintf(format, args...))
}

// ValidateURL checks a URL at registration. Host names are checked again on
// every connection, against the address they resolve to then.
func (p TargetPolicy) ValidateURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > MaxURLLen {
		return nil, forbidden("URL must be 1 to %d bytes", MaxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, forbidden("not a valid URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && p.AllowHTTP:
	default:
		return nil, forbidden("scheme must be https")
	}
	if u.User != nil {
		return nil, forbidden("credentials in the URL are not allowed")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, forbidden("fragments are not allowed")
	}
	host := u.Hostname()
	if host == "" {
		return nil, forbidden("host is required")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, forbidden("invalid port")
		}
	}
	if len(u.String()) > MaxURLLen {
		return nil, forbidden("URL must be at most %d bytes once escaped", MaxURLLen)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if err := p.checkIP(ip); err != nil {
			return nil, err
		}
	} else if numericHost(host) {
		// 2130706433, 0177.0.0.1, 0x7f.1: resolvers may read these as IPv4.
		return nil, forbidden("IPv4 addresses must be written as four decimal numbers")
	} else if h := strings.ToLower(strings.TrimSuffix(host, ".")); (h == "localhost" || strings.HasSuffix(h, ".localhost")) && !p.AllowPrivate {
		return nil, forbidden("loopback host")
	}
	return u, nil
}

// numericHost reports a host made only of digits, dots and hex notation
// that is not a canonical IP address.
func numericHost(h string) bool {
	h = strings.ToLower(h)
	for label := range strings.SplitSeq(h, ".") {
		if label == "" {
			continue
		}
		digits := strings.TrimPrefix(label, "0x")
		if strings.Trim(digits, "0123456789abcdef") != "" || (digits == label && strings.Trim(label, "0123456789") != "") {
			return false
		}
	}
	return true
}

var (
	// Never targets, even where private networks are allowed: cloud
	// metadata services outside link-local, IETF protocol assignments,
	// reserved space, and IPv6 forms that tunnel to arbitrary IPv4 hosts.
	alwaysRefused = []netip.Prefix{
		netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS instance metadata (IPv6)
		netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud metadata
		netip.MustParsePrefix("192.0.0.0/24"),       // IETF protocol assignments
		netip.MustParsePrefix("240.0.0.0/4"),        // reserved
		netip.MustParsePrefix("::/96"),              // IPv4-compatible (deprecated)
		netip.MustParsePrefix("64:ff9b:1::/48"),     // local-use NAT64
		netip.MustParsePrefix("2001::/32"),          // Teredo
	}
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	nat64       = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour   = netip.MustParsePrefix("2002::/16")
	siteLocal   = netip.MustParsePrefix("fec0::/10")
	broadcast   = netip.MustParseAddr("255.255.255.255")
)

// checkIP applies the policy to one address. IPv4-mapped, NAT64 and 6to4
// addresses are judged by the IPv4 address they carry.
func (p TargetPolicy) checkIP(ip netip.Addr) error {
	ip = ip.Unmap()
	if ip.Is6() {
		b := ip.As16()
		switch {
		case nat64.Contains(ip):
			return p.checkIP(netip.AddrFrom4([4]byte(b[12:16])))
		case sixToFour.Contains(ip):
			return p.checkIP(netip.AddrFrom4([4]byte(b[2:6])))
		}
	}
	if ip.IsLoopback() && ip.Is6() {
		// ::1 lies in ::/96 but is plain loopback.
		if !p.AllowPrivate {
			return forbidden("private or loopback address %s", ip)
		}
		return nil
	}
	for _, r := range alwaysRefused {
		if r.Contains(ip) {
			return forbidden("address %s is never a webhook target", ip)
		}
	}
	switch {
	case !ip.IsValid(), ip.IsUnspecified(), thisNetwork.Contains(ip), ip == broadcast,
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return forbidden("address %s is never a webhook target", ip)
	case ip.IsLoopback(), ip.IsPrivate(), cgnat.Contains(ip), siteLocal.Contains(ip):
		if !p.AllowPrivate {
			return forbidden("private or loopback address %s", ip)
		}
	}
	return nil
}

// Client returns an HTTP client that checks every connection's resolved
// address, never uses a proxy (which would hide the target from the check)
// and never follows redirects. timeout bounds a whole request; values of
// zero or less mean the default of 10 seconds, never "no limit".
func (p TargetPolicy) Client(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultClientTimeout
	}
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return forbidden("unexpected dial address")
			}
			return p.checkIP(ap.Addr())
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			ForceAttemptHTTP2:      true,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  timeout,
			MaxIdleConnsPerHost:    2,
			MaxResponseHeaderBytes: 16 << 10,
			IdleConnTimeout:        90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
