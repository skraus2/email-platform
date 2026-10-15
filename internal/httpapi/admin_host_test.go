package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowHosts(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := allowHosts("10.1.2.3", ok)
	for host, allowed := range map[string]bool{
		"127.0.0.1": true, "127.0.0.1:8081": true, "[::1]": true, "[::1]:8081": true, "localhost": true,
		"LOCALHOST:8081": true, "10.1.2.3": true, "10.1.2.3:8081": true, "127.0.0.2:1": true,
		"evil.example.test": false, "localhost.": false, "127.0.0.1.": false, "": false,
		"10.1.2.4:8081": false, "attacker.localhost": false, "[::1": false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/me", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Code == http.StatusNoContent; got != allowed {
			t.Errorf("Host %q: allowed=%v, want %v (code %d)", host, got, allowed, rec.Code)
		}
	}
}
