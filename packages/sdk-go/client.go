package emailplatform

import (
	"log/slog"
	"net/http"
	"regexp"
)

var apiKeyPattern = regexp.MustCompile(`^[\x21-\x7e]+$`)

// Client calls the Email Platform /v1 API. It is safe for concurrent use.
// The API key is kept unexported and is left out of every way the client
// prints or logs itself.
type Client struct {
	c *core

	Emails           *EmailsService
	Contacts         *ContactsService
	Audiences        *AudiencesService
	Unsubscribes     *UnsubscribesService
	Broadcasts       *BroadcastsService
	Automations      *AutomationsService
	AutomationRuns   *AutomationRunsService
	ContactEvents    *ContactEventsService
	Usage            *UsageService
	Templates        *TemplatesService
	WebhookEndpoints *WebhookEndpointsService
}

// New returns a client for the project the API key belongs to. baseURL is
// the API's origin, e.g. "https://api.example.com" (a path prefix is kept);
// plain http is accepted only for localhost, 127.0.0.1 and [::1].
func New(apiKey, baseURL string, opts ...ClientOption) (*Client, error) {
	if !apiKeyPattern.MatchString(apiKey) {
		return nil, invalid("API key is required and must be printable ASCII without spaces")
	}
	base, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	o := clientOptions{http: http.DefaultClient, timeout: defaultTimeout, maxRetries: defaultMaxRetries}
	for _, apply := range opts {
		if apply != nil {
			apply(&o)
		}
	}
	if o.err != nil {
		return nil, o.err
	}
	// A copy, so the caller's client keeps its own redirect policy.
	hc := *o.http
	hc.CheckRedirect = noRedirect
	authorization := "Bearer " + apiKey
	authorize := func(h http.Header) { h.Set("Authorization", authorization) }
	c := &core{authorize: authorize, base: base, timeout: o.timeout, maxRetries: o.maxRetries, http: &hc, rt: realRuntime}
	return &Client{
		c:                c,
		Emails:           &EmailsService{c},
		Contacts:         &ContactsService{c},
		Audiences:        &AudiencesService{c},
		Unsubscribes:     &UnsubscribesService{c},
		Broadcasts:       &BroadcastsService{c},
		Automations:      &AutomationsService{c},
		AutomationRuns:   &AutomationRunsService{c},
		ContactEvents:    &ContactEventsService{c},
		Usage:            &UsageService{c},
		Templates:        &TemplatesService{c},
		WebhookEndpoints: &WebhookEndpointsService{c},
	}, nil
}

func (cl *Client) baseURL() string {
	if cl == nil || cl.c == nil {
		return ""
	}
	return cl.c.base
}

// String shows the base URL only.
func (cl *Client) String() string { return "emailplatform.Client(" + cl.baseURL() + ")" }

// GoString shows the base URL only.
func (cl *Client) GoString() string { return cl.String() }

// LogValue shows the base URL only.
func (cl *Client) LogValue() slog.Value {
	return slog.GroupValue(slog.String("base_url", cl.baseURL()))
}
