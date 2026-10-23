// Command healthcheck is the container health probe for the distroless
// images: it requests one loopback URL and exits 0 on a 2xx answer.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: healthcheck http://127.0.0.1:<port>/<path>")
	}
	u, err := url.Parse(args[0])
	if err != nil || u.Scheme != "http" {
		return fmt.Errorf("want an http:// URL")
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		return fmt.Errorf("want host:port")
	}
	// Probes stay inside the container's network namespace.
	if ip, err := netip.ParseAddr(host); err != nil || !ip.IsLoopback() {
		return fmt.Errorf("only loopback addresses are probed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil) //nolint:gosec // loopback only, checked above
	if err != nil {
		return err
	}
	// Redirects are not followed: the probe never leaves the loopback URL.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req) //nolint:gosec // loopback only, checked above
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
