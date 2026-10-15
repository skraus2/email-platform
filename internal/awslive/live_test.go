//go:build awslive

// Package awslive_test sends real email through Amazon SES and reads the
// resulting events back from the SQS queue. It is compiled only with
// -tags=awslive and runs only with SES_LIVE_TEST=1. Every run costs money
// and needs the AWS resources set up beforehand; it is never part of a
// regular test run.
//
// Recipients are the SES mailbox simulator (usable in the sandbox, outside
// the daily quota and reputation metrics, billed like any send), plus an
// optional verified mailbox in SES_LIVE_TO.
//
// The test runs its own event poller, which consumes every message on the
// queue and drops events for emails it does not know. It therefore refuses
// any configuration set, topic or queue whose name does not start with
// testResourcePrefix, so it can never drain the queue of a real worker.
package awslive_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/awsconfig"
	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events/ses"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

// eventTimeout bounds the wait for SES events. Deliveries to the simulator
// usually arrive within a minute; complaints can take longer.
const eventTimeout = 10 * time.Minute

// testResourcePrefix marks the AWS resources reserved for this test.
const testResourcePrefix = "email-platform-test"

// rank orders the statuses an SES email moves through; an email past the
// expected rank, or failed, can never reach it.
var rank = map[string]int{"submitted": 0, "delivered": 1, "bounced": 2, "complained": 3}

type scenario struct {
	to         string
	status     string // final email status
	suppressed string // suppression reason, if any
}

func TestLiveSESSendAndEvents(t *testing.T) {
	if os.Getenv("SES_LIVE_TEST") != "1" {
		t.Skip("SES_LIVE_TEST=1 not set: live AWS test skipped")
	}
	// The same validation as the worker: SES opt-in complete, no stub
	// endpoints (this test is about the real service), endpoint overrides
	// ignored.
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MailProvider != "ses" || cfg.SES.Endpoint != "" || cfg.SES.SQSEndpoint != "" {
		t.Fatal("needs MAIL_PROVIDER=ses against AWS (no SES_ENDPOINT/SQS_ENDPOINT)")
	}
	checkTestResources(t, cfg.SES)
	checkEnvironment(t, cfg.SES.Region)
	from := os.Getenv("SES_LIVE_FROM")
	if from == "" {
		t.Fatal("SES_LIVE_FROM must name a sender verified in SES")
	}
	scenarios := []scenario{
		{to: "success@simulator.amazonses.com", status: "delivered"},
		{to: "bounce@simulator.amazonses.com", status: "bounced", suppressed: "bounce"},
		{to: "complaint@simulator.amazonses.com", status: "complained", suppressed: "complaint"},
	}
	if to := os.Getenv("SES_LIVE_TO"); to != "" {
		scenarios = append(scenarios, scenario{to: to, status: "delivered"})
	}

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(t.Output(), nil))
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	project, err := auth.CreateProject(ctx, q, "awslive")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := emails.AddSender(ctx, q, project.ID, emails.SenderAddress, from)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emails.VerifySender(ctx, q, project.ID, identity.ID); err != nil {
		t.Fatal(err)
	}

	provider, err := delivery.NewSES(ctx, delivery.SESConfig{Region: cfg.SES.Region, ConfigurationSet: cfg.SES.ConfigurationSet})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := emails.NewDispatcher(pool, provider, logger, cfg.ProviderTimeout, cfg.BodyMaxAge)
	dispatcher.RequireVerifiedSenders()
	svc := emails.NewService(pool, noJobs{}, logger, 100)
	svc.RequireVerifiedSenders()

	sent := make([]emails.Email, len(scenarios))
	for i, sc := range scenarios {
		e, err := svc.Create(ctx, project.ID, emails.CreateInput{
			From: from, To: sc.to, Subject: "email-platform live test " + sc.status,
			Text: "Live test message. No action needed.",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := dispatcher.SendEmail(ctx, jobs.SendEmailArgs{EmailID: e.ID, ProjectID: e.ProjectID},
			jobs.Attempt{JobID: int64(i + 1), Number: 1, MaxAttempts: 1}); err != nil {
			t.Fatalf("send to %s: %v", logging.MaskEmail(sc.to), err)
		}
		if s := status(t, pool, e); s != "submitted" {
			t.Fatalf("send to %s: status %s, want submitted", logging.MaskEmail(sc.to), s)
		}
		sent[i] = e
	}

	poller, err := ses.NewPoller(ctx, ses.PollerConfig{
		Region: cfg.SES.Region, QueueURL: cfg.SES.EventsQueueURL, TopicARN: cfg.SES.EventsTopicARN,
		Recorder: dispatcher.Events(), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	pollCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		poller.Run(pollCtx)
	}()
	defer func() { stop(); <-done }()

	deadline := time.Now().Add(eventTimeout)
	for i, sc := range scenarios {
		to := logging.MaskEmail(sc.to)
		for {
			s := status(t, pool, sent[i])
			if s == sc.status {
				break
			}
			if r, ok := rank[s]; !ok || r > rank[sc.status] {
				t.Fatalf("%s: status %s can never become %s", to, s, sc.status)
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: status %s after %v, want %s", to, s, eventTimeout, sc.status)
			}
			time.Sleep(2 * time.Second)
		}
		rows, err := pool.Query(ctx, "SELECT type FROM email_events WHERE email_id = $1 ORDER BY id", sent[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		types, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", logging.MaskEmail(sc.to), strings.Join(types, " → "))
		var reason string
		err = pool.QueryRow(ctx, "SELECT reason FROM suppressions WHERE project_id = $1 AND recipient = $2",
			project.ID, sc.to).Scan(&reason)
		switch {
		case sc.suppressed == "" && err == nil:
			t.Fatalf("%s: suppressed (%s), want not suppressed", to, reason)
		case sc.suppressed != "" && (err != nil || reason != sc.suppressed):
			t.Fatalf("%s: suppression %q (%v), want %s", to, reason, err, sc.suppressed)
		}
	}
}

// checkTestResources refuses resources not reserved for this test.
func checkTestResources(t *testing.T, c config.SES) {
	t.Helper()
	for name, v := range map[string]string{
		"SES_CONFIGURATION_SET": c.ConfigurationSet,
		"SES_EVENTS_QUEUE_URL":  c.EventsQueueURL[strings.LastIndexByte(c.EventsQueueURL, '/')+1:],
		"SES_EVENTS_TOPIC_ARN":  c.EventsTopicARN[strings.LastIndexByte(c.EventsTopicARN, ':')+1:],
	} {
		if !strings.HasPrefix(v, testResourcePrefix) {
			t.Fatalf("%s names %q: only resources named %s… may be used (the test drains its queue)", name, v, testResourcePrefix)
		}
	}
}

// checkEnvironment refuses long-lived credentials, extra trusted roots and
// proxies, and a database that is not local (the test creates and drops
// throwaway databases on it).
func checkEnvironment(t *testing.T, region string) {
	t.Helper()
	for _, k := range []string{
		"AWS_CA_BUNDLE", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy",
	} {
		if os.Getenv(k) != "" {
			t.Fatalf("%s is set: unset it for the live test", k)
		}
	}
	// The credentials the SDK would use, whatever their source: only
	// short-lived role credentials (SSO, assumed role) are accepted.
	cfg, err := awsconfig.Load(context.Background(), region, "")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("no AWS credentials: %v", err)
	}
	if !creds.CanExpire {
		t.Fatal("long-lived credentials refused: use short-lived role credentials (AWS_PROFILE with SSO or an assumed role)")
	}
	db, err := pgx.ParseConfig(os.Getenv(testdb.EnvVar))
	if err != nil {
		t.Fatalf("%s: %v", testdb.EnvVar, err)
	}
	hosts := []string{db.Host}
	for _, f := range db.Fallbacks {
		hosts = append(hosts, f.Host)
	}
	for _, h := range hosts {
		if h != "localhost" && h != "127.0.0.1" && h != "::1" && !strings.HasPrefix(h, "/") {
			t.Fatalf("%s must point at the local PostgreSQL (host %q)", testdb.EnvVar, h)
		}
	}
}

func status(t *testing.T, pool *pgxpool.Pool, e emails.Email) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), "SELECT status FROM emails WHERE id = $1 AND project_id = $2",
		e.ID, e.ProjectID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// noJobs: the test dispatches itself instead of through the job queue.
type noJobs struct{}

func (noJobs) EnqueueSendEmail(context.Context, pgx.Tx, jobs.SendEmailArgs) error { return nil }
