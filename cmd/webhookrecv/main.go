//go:build smoke

// Command webhookrecv plays the internal app in the smoke test: it accepts
// webhook requests on a loopback address, verifies each signature with the
// endpoint secret and appends one JSON line per request (type, event id,
// email id, valid) to an output file. It is not part of the shipped
// binaries (build tag "smoke"); scripts/smoke.sh builds and runs it.
//
// The secret is read from the file named by WEBHOOK_SECRET_FILE on every
// request (the endpoint, and with it the secret, is created after the
// receiver starts), never from flags.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/skraus2/email-platform/internal/webhooks"
)

type record struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	EmailID string `json:"email_id"`
	Valid   bool   `json:"valid"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "webhookrecv:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:18090", "loopback listen address")
	out := flag.String("out", "", "file the received requests are appended to")
	flag.Parse()
	secretFile := os.Getenv("WEBHOOK_SECRET_FILE")
	if *out == "" || secretFile == "" {
		return errors.New("-out and WEBHOOK_SECRET_FILE are required")
	}
	ap, err := netip.ParseAddrPort(*addr)
	if err != nil || !ap.Addr().IsLoopback() {
		return errors.New("-addr must be a loopback IP:port")
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		secret, err := os.ReadFile(secretFile) //nolint:gosec // path chosen by the operator running the smoke test
		if err != nil {
			http.Error(w, "no secret yet", http.StatusServiceUnavailable)
			return
		}
		var p struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Data struct {
				EmailID string `json:"email_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &p)
		rec := record{Type: p.Type, ID: p.ID, EmailID: p.Data.EmailID}
		rec.Valid = r.Header.Get("Webhook-Id") == p.ID && webhooks.Verify(strings.TrimSpace(string(secret)),
			r.Header.Get("Webhook-Timestamp"), r.Header.Get("Webhook-Signature"), body, 5*time.Minute, time.Now()) == nil
		line, _ := json.Marshal(rec)
		mu.Lock()
		_, err = f.Write(append(line, '\n'))
		mu.Unlock()
		if err != nil {
			http.Error(w, "write", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
