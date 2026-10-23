package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRun(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	redirect := httptest.NewServer(http.RedirectHandler("http://example.com/", http.StatusFound))
	defer redirect.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	if err := run([]string{ok.URL + "/readyz"}); err != nil {
		t.Errorf("healthy: %v", err)
	}
	for _, args := range [][]string{
		{bad.URL + "/readyz"},
		{redirect.URL + "/readyz"},
		{"http://example.com:80/readyz"},
		{"https://127.0.0.1:1/"},
		{"http://127.0.0.1:1/"},
		{},
		{ok.URL, ok.URL},
	} {
		if err := run(args); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}
