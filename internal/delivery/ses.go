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
	if c.ConfigurationSet == "" {
		return nil, errors.New("SES configuration set is required (it publishes the delivery events)")
	}
	cfg, err := awsconfig.Load(ctx, c.Region, c.Endpoint)
	if err != nil {
		return nil, err
	}
	return &SES{client: sesv2.NewFromConfig(cfg), configurationSet: c.ConfigurationSet}, nil
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
		return Result{}, classifySES(ctx, err)
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
// text is only inspected (SES names an unverified sender nowhere else),
// never kept.
func classifySES(ctx context.Context, err error) *Error {
	if ctx.Err() != nil {
		return &Error{Class: ClassTimeout}
	}
	// A failed send is also wrapped in a response error (status 0), so the
	// send error is checked first.
	var se *smithyhttp.RequestSendError
	if errors.As(err, &se) {
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
		var ae smithy.APIError
		if errors.As(err, &ae) {
			code, text = ae.ErrorCode(), ae.ErrorMessage()
		}
		return &Error{Class: sesClass(status, code, text), HTTPStatus: status}
	}
	var ipe smithy.InvalidParamsError
	if errors.As(err, &ipe) {
		return &Error{Class: ClassInvalid} // refused by the client before sending
	}
	// Failed before anything was sent (credentials, signing, serialization).
	return &Error{Class: ClassConnect}
}

func sesClass(status int, code, text string) string {
	switch {
	case throttleCodes[code] || status == http.StatusTooManyRequests:
		return ClassThrottled
	case authCodes[code] || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ClassAuth
	case code == "MailFromDomainNotVerifiedException",
		code == "MessageRejected" && strings.Contains(strings.ToLower(text), "not verified"):
		return ClassSenderUnverified
	case status >= 500 || status == http.StatusRequestTimeout:
		return ClassAPIUnavailable
	case status >= 400:
		return ClassAPIRejected
	default:
		return ClassProtocol // a success status whose body could not be read
	}
}
