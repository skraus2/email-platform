package delivery

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/skraus2/email-platform/internal/awsconfig"
)

// SESConfig selects the SES account region, the configuration set that
// publishes the message's events, and optionally a loopback stub endpoint.
type SESConfig struct {
	Region           string
	ConfigurationSet string
	Endpoint         string // local stub only; empty means AWS
}

// SES sends raw MIME messages through the Amazon SES v2 API. Each message
// carries the tags email_id and project_id, through which SES events are
// correlated back to the email; nothing from the client reaches the tags or
// the configuration set.
type SES struct {
	client           *sesv2.Client
	configurationSet string
}

// NewSES builds the adapter. It is only called with the SES opt-in
// configuration in place.
func NewSES(ctx context.Context, c SESConfig) (*SES, error) {
	return newSES(ctx, c)
}

// newSES lets tests replace the HTTP client.
func newSES(ctx context.Context, c SESConfig, opts ...func(*sesv2.Options)) (*SES, error) {
	if c.ConfigurationSet == "" {
		return nil, errors.New("SES configuration set is required (it publishes the delivery events)")
	}
	cfg, err := awsconfig.Load(ctx, c.Region, c.Endpoint)
	if err != nil {
		return nil, err
	}
	// Applied last: the SDK also takes endpoints from AWS_ENDPOINT_URL*,
	// and the shared AWS config, none of which is checked like SES_ENDPOINT.
	opts = append(opts, func(o *sesv2.Options) { o.BaseEndpoint = cfg.BaseEndpoint })
	return &SES{client: sesv2.NewFromConfig(cfg, opts...), configurationSet: c.ConfigurationSet}, nil
}

// Tag names on every SES message.
const (
	TagEmailID   = "email_id"
	TagProjectID = "project_id"
)

// Send implements Provider.
func (s *SES) Send(ctx context.Context, msg Message) (Result, error) {
	raw, err := buildMessage(msg, time.Now())
	if err != nil {
		return Result{}, &Error{Class: ClassInvalid}
	}
	out, err := s.client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress:     aws.String(msg.From),
		Destination:          &types.Destination{ToAddresses: []string{msg.To}},
		Content:              &types.EmailContent{Raw: &types.RawMessage{Data: raw}},
		ConfigurationSetName: aws.String(s.configurationSet),
		EmailTags: []types.MessageTag{
			{Name: aws.String(TagEmailID), Value: aws.String(msg.ID.String())},
			{Name: aws.String(TagProjectID), Value: aws.String(msg.ProjectID.String())},
		},
	})
	if err != nil {
		return Result{}, classifySES(ctx, err, msg.From)
	}
	return Result{Provider: "ses", ProviderID: aws.ToString(out.MessageId), Status: StatusSubmitted}, nil
}

var (
	throttleCodes = map[string]bool{
		"TooManyRequestsException": true, "LimitExceededException": true, "Throttling": true,
		"ThrottlingException": true, "RequestThrottled": true, "RequestLimitExceeded": true, "SlowDown": true,
	}
	authCodes = map[string]bool{
		"AccessDeniedException": true, "AccessDenied": true, "UnrecognizedClientException": true,
		"InvalidClientTokenId": true, "InvalidSignatureException": true, "SignatureDoesNotMatch": true,
		"IncompleteSignature": true, "ExpiredToken": true, "ExpiredTokenException": true,
		"MissingAuthenticationToken": true, "MissingAuthenticationTokenException": true, "NotAuthorized": true,
	}
)

// classifySES maps an SDK error to a sanitized Error. The provider's message
// text is only inspected (SES names an unverified identity nowhere else),
// never kept.
func classifySES(ctx context.Context, err error, sender string) *Error {
	if ctx.Err() != nil {
		return &Error{Class: ClassTimeout}
	}
	// A failed send is also wrapped in a response error (status 0), so the
	// send error is checked first.
	if _, ok := errors.AsType[*smithyhttp.RequestSendError](err); ok {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return &Error{Class: ClassConnect} // never reached the provider
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return &Error{Class: ClassTimeout}
		}
		return &Error{Class: ClassProtocol} // broke off mid-request: outcome unknown
	}
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) && re.HTTPStatusCode() != 0 {
		status := re.HTTPStatusCode()
		var code, text string
		if ae, ok := errors.AsType[smithy.APIError](err); ok {
			code, text = ae.ErrorCode(), ae.ErrorMessage()
		}
		return &Error{Class: sesClass(status, code, text, sender), HTTPStatus: status}
	}
	if _, ok := errors.AsType[smithy.InvalidParamsError](err); ok {
		return &Error{Class: ClassInvalid} // refused by the client before sending
	}
	// Failed before anything was sent (credentials, signing, serialization).
	return &Error{Class: ClassConnect}
}

// sesClass classifies an API error answer. "Not verified" names the failed
// identities: in the SES sandbox an unverified recipient gets the same
// rejection, so only one naming the sender counts as an unverified sender.
func sesClass(status int, code, text, sender string) string {
	text = strings.ToLower(text)
	switch {
	case throttleCodes[code] || status == http.StatusTooManyRequests:
		return ClassThrottled
	case authCodes[code] || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ClassAuth
	case code == "MailFromDomainNotVerifiedException",
		code == "MessageRejected" && strings.Contains(text, "not verified") && namesIdentity(text, strings.ToLower(sender)):
		return ClassSenderUnverified
	case status >= 500 || status == http.StatusRequestTimeout:
		return ClassAPIUnavailable
	case status >= 400:
		return ClassAPIRejected
	default:
		return ClassProtocol // a success status whose body could not be read
	}
}

// namesIdentity reports whether the identity list after the last colon of a
// lower-cased "not verified" rejection contains address exactly.
func namesIdentity(text, address string) bool {
	i := strings.LastIndex(text, ":")
	if i < 0 {
		return false
	}
	for id := range strings.SplitSeq(text[i+1:], ",") {
		if strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(id), ".")) == address {
			return true
		}
	}
	return false
}
