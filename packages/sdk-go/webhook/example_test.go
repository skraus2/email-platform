package webhook_test

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/skraus2/email-platform/packages/sdk-go/webhook"
)

// A net/http handler: the raw body with a size cap, verification, then the
// side effect and an insert-if-absent on the signed id in one transaction,
// so a repeated delivery changes nothing.
func ExampleVerify() {
	var db *sql.DB // the app's database, with processed_webhooks (id text primary key)
	secret := os.Getenv("EP_WEBHOOK_SECRET")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/email", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		} else if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		verified, err := webhook.Verify(secret, r.Header, body, time.Now())
		if err != nil {
			if e, ok := errors.AsType[*webhook.Error](err); ok && e.Reason != webhook.ReasonMissingSecret {
				w.WriteHeader(http.StatusBadRequest) // not from the platform, or changed
				return
			}
			// A configuration fault: 500, so the platform retries.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback() }()
		res, err := tx.ExecContext(r.Context(),
			"INSERT INTO processed_webhooks (id) VALUES ($1) ON CONFLICT DO NOTHING", verified.ID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		n, err := res.RowsAffected()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if n == 1 {
			_ = verified.Event // handle it, e.g. Type == emailplatform.EmailBounced, using tx
		}
		if err := tx.Commit(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent) // also for duplicates
	})

	// Time limits, so slow senders cannot hold connections open.
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	_ = server.ListenAndServe()
}
