//go:build load

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Targets of the expected volume: a 50k broadcast dispatched at ≥ 30/s
// while transactional mail stays under 5 s at p95.
const (
	targetRate = 30.0
	targetP95  = 5 * time.Second
)

// runBroadcast seeds an audience in SQL, sends one broadcast to it through
// the API and transactional emails alongside, and reports how both fared.
func runBroadcast(args []string) error {
	fs := flag.NewFlagSet("broadcast", flag.ContinueOnError)
	addr := fs.String("addr", "http://127.0.0.1:18082", "API base URL")
	contacts := fs.Int("contacts", 50_000, "audience size")
	txRate := fs.Float64("tx-rate", 5, "transactional emails per second while the broadcast drains")
	timeout := fs.Duration("timeout", 60*time.Minute, "how long to wait for the broadcast to drain")
	cancelSize := fs.Int("cancel", 10_000, "size of a second broadcast that is cancelled once fanned out (0: skip)")
	pids := fs.String("pids", "", "processes to sample memory of, as name=PID,name=PID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *contacts < 1 || *txRate <= 0 || *cancelSize < 0 || *cancelSize > *contacts {
		return errors.New("-contacts ≥ 1, -tx-rate > 0, 0 ≤ -cancel ≤ -contacts")
	}
	if err := loopbackURL(*addr); err != nil {
		return err
	}
	key := os.Getenv("LOADTEST_API_KEY")
	if key == "" {
		return errors.New("LOADTEST_API_KEY is required")
	}
	ctx := context.Background()
	pool, err := localPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	api := &apiClient{base: *addr, key: key, http: &http.Client{Timeout: 30 * time.Second}}

	// Audience and template through the API; contacts and membership in SQL
	// (the contacts API is not under test here).
	var me struct{ ID string }
	if err := api.do(ctx, "POST", "/v1/audiences", map[string]any{"name": fmt.Sprintf("load-%d", time.Now().UnixNano())}, &me); err != nil {
		return err
	}
	audience := me.ID
	seedStart := time.Now()
	if err := seedAudience(ctx, pool, audience, *contacts); err != nil {
		return err
	}
	seeded := time.Since(seedStart)
	var tmpl struct{ ID string }
	if err := api.do(ctx, "POST", "/v1/templates", map[string]any{"slug": fmt.Sprintf("load-%d", time.Now().UnixNano())}, &tmpl); err != nil {
		return err
	}
	if err := api.do(ctx, "POST", "/v1/templates/"+tmpl.ID+"/versions", map[string]any{
		"subject": "Load newsletter for {{.name}}",
		"text":    "Hello {{.name}}\n" + strings.Repeat("Newsletter body line.\n", 60) + "Unsubscribe: {{.unsubscribe_url}}",
		"html":    "<p>Hello {{.name}}</p>" + strings.Repeat("<p>Newsletter body line.</p>", 60) + `<a href="{{.unsubscribe_url}}">Unsubscribe</a>`,
		"variables": map[string]any{
			"name": map[string]any{"type": "string", "required": true}, "unsubscribe_url": map[string]any{"type": "string"},
		},
	}, nil); err != nil {
		return err
	}
	createBroadcast := func(name, audience string) (string, error) {
		var b struct{ ID string }
		err := api.do(ctx, "POST", "/v1/broadcasts", map[string]any{
			"name": name, "audience_id": audience, "from": "load@example.test", "template_id": tmpl.ID,
			"template_version": "published", "unsubscribe_url": "https://app.example.test/u?t={token}",
		}, &b)
		return b.ID, err
	}
	broadcast, err := createBroadcast("load main", audience)
	if err != nil {
		return err
	}

	sampler := startSampler(ctx, pool, parsePIDs(*pids))
	start := time.Now()
	if err := api.do(ctx, "POST", "/v1/broadcasts/"+broadcast+"/schedule", map[string]any{}, nil); err != nil {
		return err
	}

	// Transactional sends at a steady rate until the broadcast has drained.
	stopTx := make(chan struct{})
	var txMu sync.Mutex
	var txIDs []string
	txErrs := 0
	var txWG sync.WaitGroup
	txWG.Go(func() {
		tick := time.NewTicker(time.Duration(float64(time.Second) / *txRate))
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stopTx:
				return
			case <-tick.C:
			}
			var e struct{ ID string }
			err := api.do(ctx, "POST", "/v1/emails", map[string]any{
				"from": "load@example.test", "to": fmt.Sprintf("tx-%d@example.test", i%100), "subject": "Password reset", "text": "Your code: 000000",
			}, &e)
			txMu.Lock()
			if err != nil {
				txErrs++
			} else {
				txIDs = append(txIDs, e.ID)
			}
			txMu.Unlock()
		}
	})

	if err := waitBroadcast(ctx, pool, broadcast, start.Add(*timeout)); err != nil {
		close(stopTx)
		txWG.Wait()
		return err
	}
	close(stopTx)
	txWG.Wait()
	endToEnd := time.Since(start)
	txMu.Lock()
	ids := slices.Clone(txIDs)
	txMu.Unlock()
	if _, err := waitTerminal(ctx, pool, ids, 2*time.Minute); err != nil {
		return err
	}

	// GET of the full broadcast (progress counts over every recipient).
	getStart := time.Now()
	if err := api.do(ctx, "GET", "/v1/broadcasts/"+broadcast, nil, nil); err != nil {
		return err
	}
	getLatency := time.Since(getStart)

	report, err := broadcastReport(ctx, pool, broadcast)
	if err != nil {
		return err
	}
	txLat, err := handoffLatencies(ctx, pool, ids)
	if err != nil {
		return err
	}

	txStatuses, txFailed, err := statusCounts(ctx, pool, ids)
	if err != nil {
		return err
	}

	var cancelLine string
	if *cancelSize > 0 {
		if cancelLine, err = measureCancel(ctx, pool, api, createBroadcast, *cancelSize); err != nil {
			return err
		}
	}
	s := sampler.stop() // lock waits and memory cover the cancel phase too

	fmt.Printf("parameters      contacts=%d tx-rate=%.1f/s (seeded in %v)\n", *contacts, *txRate, seeded.Round(time.Millisecond))
	fmt.Printf("fan-out         %v (%d recipient emails, %.0f/s)\n", report.fanout.Round(time.Millisecond), report.recipients,
		float64(report.recipients)/report.fanout.Seconds())
	fmt.Printf("broadcast drain first→last hand-off %v; %.1f emails/s counted from the start; end to end %v\n",
		report.dispatchSpan.Round(time.Millisecond), report.rate, endToEnd.Round(time.Millisecond))
	fmt.Printf("broadcast final %s; snoozes=%s\n", report.statuses, report.snoozes)
	fmt.Printf("transactional   %d accepted (%s), %d API errors\n", len(ids), txStatuses, txErrs)
	if len(txLat) > 0 {
		fmt.Printf("                accept→hand-off of the sent ones p50=%v p95=%v p99=%v max=%v\n",
			pct(txLat, 50), pct(txLat, 95), pct(txLat, 99), pct(txLat, 100))
	}
	fmt.Printf("GET broadcast   %v with counts over %d emails\n", getLatency.Round(time.Millisecond), report.recipients)
	fmt.Printf("db lock waits   max %d sessions waiting on locks at once (%d samples)\n", s.maxLockWaits, s.samples)
	fmt.Printf("memory (RSS)    %s\n", s.memory())
	if cancelLine != "" {
		fmt.Printf("cancel          %s\n", cancelLine)
	}
	p95 := pct(txLat, 95)
	complete := report.recipients == *contacts
	ok := report.rate >= targetRate && len(txLat) > 0 && p95 < targetP95 && complete &&
		txErrs == 0 && txFailed == 0 && report.failed == 0
	fmt.Printf("verdict         target ≥ %.0f/s, transactional p95 < %v, every member reached, no errors: %s\n",
		targetRate, targetP95, map[bool]string{true: "MET", false: "MISSED"}[ok])
	if txErrs > 0 || txFailed > 0 || report.failed > 0 || !complete {
		return fmt.Errorf("%d API errors, %d failed transactional and %d failed broadcast emails, %d of %d members reached",
			txErrs, txFailed, report.failed, report.recipients, *contacts)
	}
	return nil
}

// localPool connects to DATABASE_URL, which must be local.
func localPool(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, errors.New("DATABASE_URL is not a valid PostgreSQL URL")
	}
	if !loopbackHost(cfg.ConnConfig.Host) {
		return nil, errors.New("DATABASE_URL must point at a local database")
	}
	for _, fb := range cfg.ConnConfig.Fallbacks {
		if !loopbackHost(fb.Host) {
			return nil, errors.New("DATABASE_URL must point at a local database (all hosts)")
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("cannot connect to DATABASE_URL")
	}
	return pool, nil
}

type apiClient struct {
	base, key string
	http      *http.Client
}

func (c *apiClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: transport error", method, strings.SplitN(path, "/", 4)[2])
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct{ Error struct{ Code string } }
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("%s %s: %d %s", method, strings.SplitN(path, "/", 4)[2], resp.StatusCode, e.Error.Code)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// seedAudience inserts n subscribed contacts of the key's project and adds
// them to the audience, in SQL. Tokens follow the stored format.
func seedAudience(ctx context.Context, pool *pgxpool.Pool, audience string, n int) error {
	var project string
	if err := pool.QueryRow(ctx, "SELECT project_id FROM audiences WHERE id = $1", audience).Scan(&project); err != nil {
		return fmt.Errorf("audience: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO contacts (project_id, email, status, consent_source, attributes, unsubscribe_token)
		SELECT $1, 'reader-' || i || '-' || $3 || '@example.test', 'subscribed', 'load',
		       jsonb_build_object('name', 'Reader ' || i),
		       translate(rtrim(encode(sha256(convert_to(gen_random_uuid()::text || gen_random_uuid()::text, 'UTF8')), 'base64'), '='), '+/', '-_')
		FROM generate_series(1, $2) AS i`, project, n, audience); err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audience_members (project_id, audience_id, contact_id)
		SELECT project_id, $1::uuid, id FROM contacts WHERE project_id = $2 AND email LIKE '%-' || $3::text || '@example.test'`, audience, project, audience); err != nil {
		return fmt.Errorf("seed members: %w", err)
	}
	return nil
}

// waitBroadcast polls until the broadcast completed and none of its emails
// is queued or sending.
func waitBroadcast(ctx context.Context, pool *pgxpool.Pool, id string, deadline time.Time) error {
	for {
		var status string
		var open int
		if err := pool.QueryRow(ctx, `SELECT b.status, (SELECT count(*) FROM emails e WHERE e.broadcast_id = b.id AND e.status IN ('queued', 'sending'))
			FROM broadcasts b WHERE b.id = $1`, id).Scan(&status, &open); err != nil {
			return fmt.Errorf("poll broadcast: %w", err)
		}
		if status == "completed" && open == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("broadcast %s with %d open emails at the timeout", status, open)
		}
		time.Sleep(time.Second)
	}
}

type broadcastResult struct {
	fanout, dispatchSpan time.Duration
	recipients, failed   int
	rate                 float64
	statuses             string
	snoozes              string
}

func broadcastReport(ctx context.Context, pool *pgxpool.Pool, id string) (broadcastResult, error) {
	var r broadcastResult
	var started, finished, first, last time.Time
	if err := pool.QueryRow(ctx, `SELECT started_at, finished_at, recipients FROM broadcasts WHERE id = $1`, id).
		Scan(&started, &finished, &r.recipients); err != nil {
		return r, fmt.Errorf("broadcast: %w", err)
	}
	r.fanout = finished.Sub(started)
	if err := pool.QueryRow(ctx, `SELECT min(finished_at), max(finished_at), count(*) FILTER (WHERE status = 'failed')
		FROM emails WHERE broadcast_id = $1`, id).Scan(&first, &last, &r.failed); err != nil {
		return r, fmt.Errorf("broadcast emails: %w", err)
	}
	r.dispatchSpan = last.Sub(first)
	if r.dispatchSpan > 0 {
		r.rate = float64(r.recipients) / last.Sub(started).Seconds()
	}
	rows, err := pool.Query(ctx, "SELECT status, count(*) FROM emails WHERE broadcast_id = $1 GROUP BY status ORDER BY status", id)
	if err != nil {
		return r, err
	}
	var parts []string
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return r, err
		}
		parts = append(parts, fmt.Sprintf("%s=%d", st, n))
	}
	rows.Close()
	r.statuses = strings.Join(parts, " ")
	r.snoozes = "unknown"
	var n int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum((j.metadata->>'snoozes')::int), 0) FROM river_job j
		JOIN emails e ON e.id::text = j.args->>'email_id' WHERE j.kind = 'send_email' AND e.broadcast_id = $1`, id).Scan(&n); err == nil {
		r.snoozes = strconv.FormatInt(n, 10)
	}
	return r, nil
}

// statusCounts returns the final statuses of the emails and how many failed.
func statusCounts(ctx context.Context, pool *pgxpool.Pool, ids []string) (string, int, error) {
	rows, err := pool.Query(ctx, "SELECT status, count(*) FROM emails WHERE id = ANY($1::uuid[]) GROUP BY status ORDER BY status", ids)
	if err != nil {
		return "", 0, fmt.Errorf("final states: %w", err)
	}
	defer rows.Close()
	var parts []string
	failed := 0
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return "", 0, err
		}
		parts = append(parts, fmt.Sprintf("%s=%d", st, n))
		if st == "failed" {
			failed = n
		}
	}
	return strings.Join(parts, " "), failed, rows.Err()
}

// handoffLatencies returns accept→hand-off times of the transactional
// emails that were sent, sorted.
func handoffLatencies(ctx context.Context, pool *pgxpool.Pool, ids []string) ([]time.Duration, error) {
	rows, err := pool.Query(ctx, `SELECT extract(epoch FROM finished_at - created_at) FROM emails
		WHERE id = ANY($1::uuid[]) AND status IN ('captured', 'submitted', 'delivered')`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Duration
	for rows.Next() {
		var secs float64
		if err := rows.Scan(&secs); err != nil {
			return nil, err
		}
		out = append(out, time.Duration(secs*float64(time.Second)))
	}
	slices.Sort(out)
	return out, rows.Err()
}

// measureCancel sends a second broadcast of size recipients (own
// audience), cancels it as soon as its fan-out completed, and measures
// until none of its emails is queued any more.
func measureCancel(ctx context.Context, pool *pgxpool.Pool, api *apiClient, create func(name, audience string) (string, error), size int) (string, error) {
	var aud struct{ ID string }
	if err := api.do(ctx, "POST", "/v1/audiences", map[string]any{"name": fmt.Sprintf("load-cancel-%d", time.Now().UnixNano())}, &aud); err != nil {
		return "", err
	}
	if err := seedAudience(ctx, pool, aud.ID, size); err != nil {
		return "", err
	}
	id, err := create("load cancel", aud.ID)
	if err != nil {
		return "", err
	}
	if err := api.do(ctx, "POST", "/v1/broadcasts/"+id+"/schedule", map[string]any{}, nil); err != nil {
		return "", err
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		var status string
		if err := pool.QueryRow(ctx, "SELECT status FROM broadcasts WHERE id = $1", id).Scan(&status); err != nil {
			return "", err
		}
		if status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("cancel broadcast still %s after 10 min", status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	start := time.Now()
	if err := api.do(ctx, "POST", "/v1/broadcasts/"+id+"/cancel", nil, nil); err != nil {
		return "", err
	}
	cancelCall := time.Since(start)
	for {
		var open, canceled, sent int
		if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('queued', 'sending')),
			count(*) FILTER (WHERE status = 'canceled'), count(*) FILTER (WHERE status IN ('captured', 'submitted'))
			FROM emails WHERE broadcast_id = $1`, id).Scan(&open, &canceled, &sent); err != nil {
			return "", err
		}
		if open == 0 {
			events := "unknown"
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_events ev JOIN emails e ON e.id = ev.email_id
				WHERE e.broadcast_id = $1 AND ev.type = 'canceled'`, id).Scan(&n); err == nil {
				events = strconv.Itoa(n)
			}
			return fmt.Sprintf("broadcast of %d cancelled after fan-out: API call %v, queued emails ended in %v (%d canceled with %s events, %d already sent)",
				size, cancelCall.Round(time.Millisecond), time.Since(start).Round(time.Millisecond), canceled, events, sent), nil
		}
		if time.Since(start) > 10*time.Minute {
			return "", fmt.Errorf("%d emails still open 10 min after cancel", open)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
