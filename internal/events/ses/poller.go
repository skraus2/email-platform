package ses

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/awsconfig"
	"github.com/skraus2/email-platform/internal/events"
)

// PollerConfig selects the queue the SES events arrive in, the SNS topic
// that must have signed them, and optionally a loopback stub endpoint.
type PollerConfig struct {
	Region   string
	QueueURL string
	TopicARN string
	Endpoint string // local stub only; empty means AWS
	Recorder *events.Recorder
	Logger   *slog.Logger
	// MessageTimeout bounds the handling of one received message; at most
	// (and by default) maxMessageTimeout. Keep it within the shutdown
	// timeout: a message being handled is finished on shutdown.
	MessageTimeout time.Duration
}

// Poller receives SES event notifications from SQS, authenticates and
// parses them and hands them to the event recorder. A message is deleted
// only once its outcome is final (stored, duplicate, or refused for good);
// after a transient failure it stays on the queue and comes back after the
// visibility timeout. Messages that never succeed are moved aside by the
// queue's redrive policy.
type Poller struct {
	client         *sqs.Client
	queueURL       string
	verifier       *Verifier
	account        string
	ingest         func(context.Context, events.ProviderEvent) (events.Outcome, error)
	logger         *slog.Logger
	messageTimeout time.Duration
	backoff        func(failures int) time.Duration
}

const (
	waitSeconds       = 20 // SQS long-poll maximum
	maxMessages       = 10 // SQS batch maximum
	maxMessageTimeout = 10 * time.Second
	// The whole batch is handled before its messages could reappear.
	visibilityTimeout = 2 * maxMessages * maxMessageTimeout
	receiveTimeout    = (waitSeconds + 10) * time.Second
	maxBackoff        = 30 * time.Second
	// alertReceiveCount: a message left for redelivery this often is logged
	// as an error (it will end in the dead-letter queue unless fixed).
	alertReceiveCount = 10
)

// NewPoller builds the poller. It is only called with the SES opt-in
// configuration in place. Signing certificates are only ever fetched from
// the pinned SNS host (NewVerifier).
func NewPoller(ctx context.Context, c PollerConfig) (*Poller, error) {
	if c.Recorder == nil {
		return nil, errors.New("event recorder is required")
	}
	v, err := NewVerifier(c.Region, c.TopicARN)
	if err != nil {
		return nil, err
	}
	p, err := newPoller(ctx, c, v)
	if err != nil {
		return nil, err
	}
	p.ingest = c.Recorder.Ingest
	return p, nil
}

// newPoller lets tests replace the verifier's certificate fetch, the HTTP
// client and the ingest (which the caller sets).
func newPoller(ctx context.Context, c PollerConfig, v *Verifier, opts ...func(*sqs.Options)) (*Poller, error) {
	if c.QueueURL == "" {
		return nil, errors.New("SQS queue URL is required")
	}
	if c.Logger == nil {
		return nil, errors.New("logger is required")
	}
	account, ok := topicAccount(c.TopicARN)
	if !ok {
		return nil, errors.New("topic ARN must be an SNS topic ARN")
	}
	timeout := c.MessageTimeout
	if timeout <= 0 || timeout > maxMessageTimeout {
		timeout = maxMessageTimeout
	}
	cfg, err := awsconfig.Load(ctx, c.Region, c.Endpoint)
	if err != nil {
		return nil, err
	}
	// Applied last: the SDK also takes endpoints from AWS_ENDPOINT_URL*,
	// and the shared AWS config, none of which is checked like SQS_ENDPOINT.
	// The body checksum validation stays on.
	opts = append(opts, func(o *sqs.Options) { o.BaseEndpoint = cfg.BaseEndpoint })
	return &Poller{
		client: sqs.NewFromConfig(cfg, opts...), queueURL: c.QueueURL, verifier: v, account: account,
		logger: c.Logger, messageTimeout: timeout, backoff: receiveBackoff,
	}, nil
}

// topicAccount returns the account id of an SNS topic ARN.
func topicAccount(arn string) (string, bool) {
	if _, ok := TopicRegion(arn); !ok {
		return "", false
	}
	return strings.Split(arn, ":")[4], true
}

// receiveBackoff waits 1 s, 2 s, 4 s … up to maxBackoff after consecutive
// failed rounds.
func receiveBackoff(failures int) time.Duration {
	if failures > 5 {
		return maxBackoff
	}
	return min(time.Second<<(failures-1), maxBackoff)
}

// Run polls until ctx is cancelled. A message already received when that
// happens is finished; the rest of its batch stays on the queue.
func (p *Poller) Run(ctx context.Context) {
	p.logger.InfoContext(ctx, "ses event poller started")
	failures := 0
	for ctx.Err() == nil {
		if err := p.poll(ctx); err != nil {
			if ctx.Err() != nil {
				break
			}
			failures++
			delay := p.backoff(failures)
			p.logger.WarnContext(ctx, "ses event poll failed", slog.String("error", err.Error()),
				slog.Duration("retry_in", delay))
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
			continue
		}
		failures = 0
	}
	p.logger.Info("ses event poller stopped")
}

// errTransient reports that at least one message of the batch was left on
// the queue after a transient failure.
var errTransient = errors.New("messages left for redelivery")

// poll receives one batch and handles each message. It returns an error if
// the receive failed or a message was left for redelivery.
func (p *Poller) poll(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, receiveTimeout)
	defer cancel()
	out, err := p.client.ReceiveMessage(rctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(p.queueURL),
		MaxNumberOfMessages:         maxMessages,
		WaitTimeSeconds:             waitSeconds,
		VisibilityTimeout:           int32(visibilityTimeout / time.Second),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		// The SDK error may quote the provider's message; only its kind is
		// reported.
		return fmt.Errorf("receive: %s", awsErrorKind(err))
	}
	var result error
	for _, m := range out.Messages {
		if ctx.Err() != nil {
			break // shutdown: the rest comes back after the visibility timeout
		}
		if !p.handle(ctx, m) {
			result = errTransient
		}
	}
	return result
}

// handle processes one message in its own context, which shutdown does not
// cancel, and reports whether its outcome was final.
func (p *Poller) handle(ctx context.Context, m types.Message) bool {
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.messageTimeout)
	defer cancel()
	r := p.process(mctx, aws.ToString(m.Body))
	attrs := []any{slog.String("sqs_message_id", aws.ToString(m.MessageId)), slog.String("outcome", r.outcome)}
	receives, err := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	if err == nil {
		attrs = append(attrs, slog.Int("receive_count", receives))
	}
	if r.event != nil {
		attrs = append(attrs, slog.String("project_id", r.event.ProjectID.String()), slog.String("email_id", r.event.EmailID.String()))
	}
	if r.reason != "" {
		attrs = append(attrs, slog.String("reason", r.reason))
	}
	if r.sqlstate != "" {
		attrs = append(attrs, slog.String("sqlstate", r.sqlstate))
	}
	log := p.logger.With(attrs...)
	switch {
	case !r.final && receives == alertReceiveCount:
		log.ErrorContext(mctx, "ses event keeps failing")
		return false
	case !r.final:
		log.WarnContext(mctx, "ses event left for redelivery")
		return false
	case r.reason != "":
		log.WarnContext(mctx, "ses event refused")
	default:
		log.DebugContext(mctx, "ses event handled")
	}
	if _, err := p.client.DeleteMessage(mctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(p.queueURL), ReceiptHandle: m.ReceiptHandle,
	}); err != nil {
		// Comes back after the visibility timeout; the ingest deduplicates it.
		log.WarnContext(mctx, "ses event delete failed", slog.String("error", awsErrorKind(err)))
	}
	return true
}

// result of handling one message: the outcome, a reason for refusals and
// failures, whether it is final, and the parsed event if there is one.
type result struct {
	outcome, reason string
	final           bool
	event           *events.ProviderEvent
	sqlstate        string // of a failed ingest, if the database answered
}

// process authenticates, parses and ingests a message body.
func (p *Poller) process(ctx context.Context, body string) result {
	msg, err := p.verifier.Verify(ctx, []byte(body))
	if errors.Is(err, ErrCertFetch) {
		return result{outcome: "certificate_unavailable", reason: "signing certificate could not be fetched"}
	}
	if err != nil {
		return result{outcome: "unauthenticated", reason: sentinelText(err), final: true}
	}
	ev, account, err := parse(msg)
	switch {
	case errors.Is(err, ErrIgnored):
		return result{outcome: "ignored", final: true}
	case err != nil:
		return result{outcome: "invalid", reason: sentinelText(err), final: true}
	}
	// A topic may receive events from several accounts' configuration sets
	// if its policy allows; only the platform's own account counts.
	if account == "" || account != p.account {
		return result{outcome: "foreign_account", reason: "event sent by another AWS account", final: true, event: &ev}
	}
	out, err := p.ingest(ctx, ev)
	switch {
	case errors.Is(err, events.ErrInvalidEvent):
		return result{outcome: "invalid", reason: "refused by the event store", final: true, event: &ev}
	case err != nil:
		// Only the code: the server's message and detail can quote row
		// values, and connection errors name hosts and users.
		var pgErr *pgconn.PgError
		state := ""
		if errors.As(err, &pgErr) {
			state = pgErr.Code
		}
		return result{outcome: "ingest_failed", reason: "event store unavailable", event: &ev, sqlstate: state}
	}
	return result{outcome: out.String(), final: true, event: &ev}
}

// sentinelText returns the fixed text of a rejection without any detail
// wrapped around it.
func sentinelText(err error) string {
	for _, s := range []error{
		ErrMalformed, ErrNotNotification, ErrSignatureVersion, ErrWrongTopic, ErrCertURL, ErrCertificate,
		ErrBadSignature, ErrInvalid,
	} {
		if errors.Is(err, s) {
			return s.Error()
		}
	}
	return "rejected"
}

var errorCodePattern = regexp.MustCompile(`^[A-Za-z0-9._#-]{1,64}$`)

// awsErrorKind describes an SDK error without the provider's message text:
// the HTTP status and error code of an answer, or the transport failure.
func awsErrorKind(err error) string {
	var re *smithyhttp.ResponseError
	var se *smithyhttp.RequestSendError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.As(err, &se):
		return "connect"
	case errors.As(err, &re) && re.HTTPStatusCode() != 0:
		kind := "http_" + strconv.Itoa(re.HTTPStatusCode())
		var ae smithy.APIError
		if errors.As(err, &ae) && errorCodePattern.MatchString(ae.ErrorCode()) {
			kind += " " + ae.ErrorCode()
		}
		return kind
	}
	return "request_failed"
}
