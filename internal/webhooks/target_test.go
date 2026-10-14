package webhooks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	strict := TargetPolicy{}
	lax := TargetPolicy{AllowHTTP: true, AllowPrivate: true}
	for _, ok := range []string{
		"https://hooks.example.test/email",
		"https://hooks.example.test:8443/a?token=x",
		"https://93.184.215.14/hook",
		"https://[2606:2800:21f:cb07:6820:80da:af6b:8b2c]/hook",
	} {
		if _, err := strict.ValidateURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"http://hooks.example.test/",          // http not allowed
		"ftp://hooks.example.test/",           // other scheme
		"https://user:pw@hooks.example.test/", // userinfo
		"https://hooks.example.test/#frag",
		"https:///nohost",
		"hooks.example.test/path",
		"https://" + strings.Repeat("a", 2040) + ".test/",
		"https://127.0.0.1/",              // loopback literal
		"https://10.0.0.1/",               // private literal
		"https://169.254.169.254/latest/", // metadata
		"https://[::ffff:169.254.169.254]/",
		"https://0.0.0.0/",
		"https://localhost/", // name that is loopback by definition
		"https://hooks.localhost/",
		"https://hooks.example.test:0/",
		"https://hooks.example.test:99999/",
	} {
		if _, err := strict.ValidateURL(bad); err == nil {
			t.Errorf("%q accepted by the strict policy", bad)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:9000/hook", "https://10.0.0.1/", "http://localhost:8080/"} {
		if _, err := lax.ValidateURL(ok); err != nil {
			t.Errorf("%s with private+http allowed: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://169.254.169.254/", "http://[fe80::1]/", "https://224.0.0.1/", "https://[::]/"} {
		if _, err := lax.ValidateURL(bad); err == nil {
			t.Errorf("%q accepted although always forbidden", bad)
		}
	}
}

func TestCheckIP(t *testing.T) {
	always := []string{
		"0.0.0.0", "0.1.2.3", "169.254.169.254", "169.254.0.1", "224.0.0.1", "255.255.255.255",
		"::", "fe80::1", "ff02::1", "::ffff:169.254.169.254", "::ffff:0.0.0.0",
		"64:ff9b::a9fe:a9fe", // NAT64 of 169.254.169.254
	}
	private := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "100.64.0.1", "::1", "fc00::1", "fd12::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::a00:1", "2002:a00:1::1", "fec0::1",
	}
	public := []string{"93.184.215.14", "2606:2800:21f:cb07:6820:80da:af6b:8b2c", "8.8.8.8"}
	for _, p := range []TargetPolicy{{}, {AllowPrivate: true}} {
		for _, s := range always {
			if err := p.checkIP(netip.MustParseAddr(s)); !errors.Is(err, ErrForbiddenTarget) {
				t.Errorf("policy %+v: %s allowed", p, s)
			}
		}
		for _, s := range public {
			if err := p.checkIP(netip.MustParseAddr(s)); err != nil {
				t.Errorf("policy %+v: %s refused: %v", p, s, err)
			}
		}
	}
	for _, s := range private {
		if err := (TargetPolicy{}).checkIP(netip.MustParseAddr(s)); !errors.Is(err, ErrForbiddenTarget) {
			t.Errorf("strict: private %s allowed", s)
		}
		if err := (TargetPolicy{AllowPrivate: true}).checkIP(netip.MustParseAddr(s)); err != nil {
			t.Errorf("allow-private: %s refused: %v", s, err)
		}
	}
}

// The dial-time check sees the resolved address, never a proxy, and
// redirects are not followed.
func TestClientChecksResolvedAddress(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:3128")
	t.Setenv("HTTPS_PROXY", "http://192.0.2.1:3128")

	strict := TargetPolicy{AllowHTTP: true}.Client(0)
	if strict.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("transport uses a proxy")
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/hook", nil)
	resp, err := strict.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrForbiddenTarget) {
		t.Fatalf("loopback with the strict policy: err = %v, want ErrForbiddenTarget", err)
	}

	lax := TargetPolicy{AllowHTTP: true, AllowPrivate: true}.Client(0)
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/redirect", nil)
	resp, err = lax.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits != 1 {
		t.Fatalf("status %d after %d hits, want the 302 itself and no follow-up", resp.StatusCode, hits)
	}
}
