package emailplatform

import (
	"context"
	"iter"
	"strconv"
)

// One method per /v1 operation, grouped by resource. Request and response
// types are the generated contract types, so field names stay as they are
// on the wire. Every method returns an error before any request for an
// invalid argument; see APIError and TransportError for the others.

func requestOptions(opts []RequestOption) []func(*callOptions) {
	out := make([]func(*callOptions), 0, len(opts))
	for _, o := range opts {
		if o != nil {
			out = append(out, o)
		}
	}
	return out
}

func idempotentOptions(opts []IdempotentOption) []func(*callOptions) {
	out := make([]func(*callOptions), 0, len(opts))
	for _, o := range opts {
		if f, ok := o.(RequestOption); o == nil || (ok && f == nil) {
			continue
		}
		out = append(out, o.applyIdempotent)
	}
	return out
}

// requiredBody turns a nil body into an argument error; a non-nil pointer is
// passed on as is.
func requiredBody[T any](b *T) (any, error) {
	if b == nil {
		return nil, invalid("body is required")
	}
	return b, nil
}

// optionalBody sends no body at all for nil (not JSON null).
func optionalBody[T any](b *T) any {
	if b == nil {
		return nil
	}
	return b
}

func callFor[T any](ctx context.Context, c *core, id string, r request, opts []func(*callOptions)) (*T, error) {
	var out T
	r.result = &out
	if err := c.call(ctx, id, r, opts); err != nil {
		return nil, err
	}
	return &out, nil
}

func versionParam(version int) (string, error) {
	if version < 1 {
		return "", invalid("version must be a positive integer")
	}
	return strconv.Itoa(version), nil
}

// EmailsService holds the emails operations; use it through Client.Emails.
type EmailsService struct{ c *core }

// String names the service only.
func (s *EmailsService) String() string { return "emailplatform.EmailsService" }

// Send queues an email (raw content or a template). Retries after a lost connection, a 429 or a 500/502/503/504 reuse one Idempotency-Key, so they cannot queue a second email.
func (s *EmailsService) Send(ctx context.Context, body *CreateEmailRequest, opts ...IdempotentOption) (*CreateEmailResponse, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[CreateEmailResponse](ctx, s.c, "createEmail", request{body: b}, idempotentOptions(opts))
}

// List returns one page of emails, newest first.
func (s *EmailsService) List(ctx context.Context, params *ListEmailsParams, opts ...RequestOption) (*EmailList, error) {
	return callFor[EmailList](ctx, s.c, "listEmails", request{params: params}, requestOptions(opts))
}

// ListAll walks every Email of every page, starting at the first page
// on each range (a Cursor in params is ignored, params is not modified).
// Each page is its own call with ctx and opts; an error ends the walk.
func (s *EmailsService) ListAll(ctx context.Context, params *ListEmailsParams, opts ...RequestOption) iter.Seq2[Email, error] {
	return paginate(func(cursor *string) ([]Email, *string, error) {
		var p ListEmailsParams
		if params != nil {
			p = *params
		}
		p.Cursor = cursor
		page, err := s.List(ctx, &p, opts...)
		if err != nil {
			return nil, nil, err
		}
		return page.Data, page.NextCursor, nil
	})
}

// Get returns an email with its current status.
func (s *EmailsService) Get(ctx context.Context, id string, opts ...RequestOption) (*Email, error) {
	return callFor[Email](ctx, s.c, "getEmail", request{pathParams: []string{id}}, requestOptions(opts))
}

// ListEvents returns one page of an email's events.
func (s *EmailsService) ListEvents(ctx context.Context, id string, params *ListEmailEventsParams, opts ...RequestOption) (*EmailEventList, error) {
	return callFor[EmailEventList](ctx, s.c, "listEmailEvents", request{pathParams: []string{id}, params: params}, requestOptions(opts))
}

// ListAllEvents walks every EmailEvent of every page, starting at the first page
// on each range (a Cursor in params is ignored, params is not modified).
// Each page is its own call with ctx and opts; an error ends the walk.
func (s *EmailsService) ListAllEvents(ctx context.Context, id string, params *ListEmailEventsParams, opts ...RequestOption) iter.Seq2[EmailEvent, error] {
	return paginate(func(cursor *string) ([]EmailEvent, *string, error) {
		var p ListEmailEventsParams
		if params != nil {
			p = *params
		}
		p.Cursor = cursor
		page, err := s.ListEvents(ctx, id, &p, opts...)
		if err != nil {
			return nil, nil, err
		}
		return page.Data, page.NextCursor, nil
	})
}

// Cancel cancels a scheduled email that has not been sent.
func (s *EmailsService) Cancel(ctx context.Context, id string, opts ...RequestOption) (*Email, error) {
	return callFor[Email](ctx, s.c, "cancelEmail", request{pathParams: []string{id}}, requestOptions(opts))
}

// ContactsService holds the contacts operations; use it through Client.Contacts.
type ContactsService struct{ c *core }

// String names the service only.
func (s *ContactsService) String() string { return "emailplatform.ContactsService" }

// Create creates a contact.
func (s *ContactsService) Create(ctx context.Context, body *CreateContactRequest, opts ...RequestOption) (*Contact, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[Contact](ctx, s.c, "createContact", request{body: b}, requestOptions(opts))
}

// List returns one page of contacts.
func (s *ContactsService) List(ctx context.Context, params *ListContactsParams, opts ...RequestOption) (*ContactList, error) {
	return callFor[ContactList](ctx, s.c, "listContacts", request{params: params}, requestOptions(opts))
}

// ListAll walks every Contact of every page, starting at the first page
// on each range (a Cursor in params is ignored, params is not modified).
// Each page is its own call with ctx and opts; an error ends the walk.
func (s *ContactsService) ListAll(ctx context.Context, params *ListContactsParams, opts ...RequestOption) iter.Seq2[Contact, error] {
	return paginate(func(cursor *string) ([]Contact, *string, error) {
		var p ListContactsParams
		if params != nil {
			p = *params
		}
		p.Cursor = cursor
		page, err := s.List(ctx, &p, opts...)
		if err != nil {
			return nil, nil, err
		}
		return page.Data, page.NextCursor, nil
	})
}

// Get returns a contact.
func (s *ContactsService) Get(ctx context.Context, id string, opts ...RequestOption) (*Contact, error) {
	return callFor[Contact](ctx, s.c, "getContact", request{pathParams: []string{id}}, requestOptions(opts))
}

// Update changes a contact.
func (s *ContactsService) Update(ctx context.Context, id string, body *UpdateContactRequest, opts ...RequestOption) (*Contact, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[Contact](ctx, s.c, "updateContact", request{pathParams: []string{id}, body: b}, requestOptions(opts))
}

// Delete deletes a contact.
func (s *ContactsService) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	return s.c.call(ctx, "deleteContact", request{pathParams: []string{id}}, requestOptions(opts))
}

// AudiencesService holds the audiences operations; use it through Client.Audiences.
type AudiencesService struct{ c *core }

// String names the service only.
func (s *AudiencesService) String() string { return "emailplatform.AudiencesService" }

// Create creates an audience.
func (s *AudiencesService) Create(ctx context.Context, body *CreateAudienceRequest, opts ...RequestOption) (*Audience, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[Audience](ctx, s.c, "createAudience", request{body: b}, requestOptions(opts))
}

// List returns one page of audiences.
func (s *AudiencesService) List(ctx context.Context, params *ListAudiencesParams, opts ...RequestOption) (*AudienceList, error) {
	return callFor[AudienceList](ctx, s.c, "listAudiences", request{params: params}, requestOptions(opts))
}

// ListAll walks every Audience of every page, starting at the first page
// on each range (a Cursor in params is ignored, params is not modified).
// Each page is its own call with ctx and opts; an error ends the walk.
func (s *AudiencesService) ListAll(ctx context.Context, params *ListAudiencesParams, opts ...RequestOption) iter.Seq2[Audience, error] {
	return paginate(func(cursor *string) ([]Audience, *string, error) {
		var p ListAudiencesParams
		if params != nil {
			p = *params
		}
		p.Cursor = cursor
		page, err := s.List(ctx, &p, opts...)
		if err != nil {
			return nil, nil, err
		}
		return page.Data, page.NextCursor, nil
	})
}

// Get returns an audience with its member count.
func (s *AudiencesService) Get(ctx context.Context, id string, opts ...RequestOption) (*AudienceWithCount, error) {
	return callFor[AudienceWithCount](ctx, s.c, "getAudience", request{pathParams: []string{id}}, requestOptions(opts))
}

// AddContacts adds several contacts to an audience.
func (s *AudiencesService) AddContacts(ctx context.Context, id string, body *AddAudienceContactsRequest, opts ...RequestOption) (*AddAudienceContactsResult, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[AddAudienceContactsResult](ctx, s.c, "addAudienceContacts", request{pathParams: []string{id}, body: b}, requestOptions(opts))
}

// ListContacts returns one page of an audience's contacts.
func (s *AudiencesService) ListContacts(ctx context.Context, id string, params *ListAudienceContactsParams, opts ...RequestOption) (*ContactList, error) {
	return callFor[ContactList](ctx, s.c, "listAudienceContacts", request{pathParams: []string{id}, params: params}, requestOptions(opts))
}

// ListAllContacts walks every Contact of every page, starting at the first page
// on each range (a Cursor in params is ignored, params is not modified).
// Each page is its own call with ctx and opts; an error ends the walk.
func (s *AudiencesService) ListAllContacts(ctx context.Context, id string, params *ListAudienceContactsParams, opts ...RequestOption) iter.Seq2[Contact, error] {
	return paginate(func(cursor *string) ([]Contact, *string, error) {
		var p ListAudienceContactsParams
		if params != nil {
			p = *params
		}
		p.Cursor = cursor
		page, err := s.ListContacts(ctx, id, &p, opts...)
		if err != nil {
			return nil, nil, err
		}
		return page.Data, page.NextCursor, nil
	})
}

// AddContact adds one contact to an audience.
func (s *AudiencesService) AddContact(ctx context.Context, id string, contactID string, opts ...RequestOption) error {
	return s.c.call(ctx, "addAudienceContact", request{pathParams: []string{id, contactID}}, requestOptions(opts))
}

// RemoveContact removes a contact from an audience.
func (s *AudiencesService) RemoveContact(ctx context.Context, id string, contactID string, opts ...RequestOption) error {
	return s.c.call(ctx, "removeAudienceContact", request{pathParams: []string{id, contactID}}, requestOptions(opts))
}

// UnsubscribesService holds the unsubscribes operations; use it through Client.Unsubscribes.
type UnsubscribesService struct{ c *core }

// String names the service only.
func (s *UnsubscribesService) String() string { return "emailplatform.UnsubscribesService" }

// Report reports an unsubscribe received by the app's unsubscribe page.
func (s *UnsubscribesService) Report(ctx context.Context, body *UnsubscribeRequest, opts ...RequestOption) (*UnsubscribeResult, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[UnsubscribeResult](ctx, s.c, "reportUnsubscribe", request{body: b}, requestOptions(opts))
}

// BroadcastsService holds the broadcasts operations; use it through Client.Broadcasts.
type BroadcastsService struct{ c *core }

// String names the service only.
func (s *BroadcastsService) String() string { return "emailplatform.BroadcastsService" }

// Create creates a broadcast draft. A 503 or timeout leaves the outcome unknown and is not retried: list before creating again.
func (s *BroadcastsService) Create(ctx context.Context, body *CreateBroadcastRequest, opts ...RequestOption) (*Broadcast, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[Broadcast](ctx, s.c, "createBroadcast", request{body: b}, requestOptions(opts))
}

// List returns one page of broadcasts.
func (s *BroadcastsService) List(ctx context.Context, params *ListBroadcastsParams, opts ...RequestOption) (*BroadcastList, error) {
	return callFor[BroadcastList](ctx, s.c, "listBroadcasts", request{params: params}, requestOptions(opts))
}

// ListAll walks every Broadcast of every page, starting at the first page
// on each range (a Cursor in params is ignored, params is not modified).
// Each page is its own call with ctx and opts; an error ends the walk.
func (s *BroadcastsService) ListAll(ctx context.Context, params *ListBroadcastsParams, opts ...RequestOption) iter.Seq2[Broadcast, error] {
	return paginate(func(cursor *string) ([]Broadcast, *string, error) {
		var p ListBroadcastsParams
		if params != nil {
			p = *params
		}
		p.Cursor = cursor
		page, err := s.List(ctx, &p, opts...)
		if err != nil {
			return nil, nil, err
		}
		return page.Data, page.NextCursor, nil
	})
}

// Get returns a broadcast with its counts.
func (s *BroadcastsService) Get(ctx context.Context, id string, opts ...RequestOption) (*BroadcastWithCounts, error) {
	return callFor[BroadcastWithCounts](ctx, s.c, "getBroadcast", request{pathParams: []string{id}}, requestOptions(opts))
}

// Schedule schedules a broadcast; a nil body sends it now.
func (s *BroadcastsService) Schedule(ctx context.Context, id string, body *ScheduleBroadcastRequest, opts ...RequestOption) (*Broadcast, error) {
	return callFor[Broadcast](ctx, s.c, "scheduleBroadcast", request{pathParams: []string{id}, body: optionalBody(body)}, requestOptions(opts))
}

// Cancel cancels a scheduled broadcast.
func (s *BroadcastsService) Cancel(ctx context.Context, id string, opts ...RequestOption) (*Broadcast, error) {
	return callFor[Broadcast](ctx, s.c, "cancelBroadcast", request{pathParams: []string{id}}, requestOptions(opts))
}

// AutomationsService holds the automations operations; use it through Client.Automations.
type AutomationsService struct{ c *core }

// String names the service only.
func (s *AutomationsService) String() string { return "emailplatform.AutomationsService" }

// Create creates an automation.
func (s *AutomationsService) Create(ctx context.Context, body *CreateAutomationRequest, opts ...RequestOption) (*Automation, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[Automation](ctx, s.c, "createAutomation", request{body: b}, requestOptions(opts))
}

// List returns one page of automations.
func (s *AutomationsService) List(ctx context.Context, params *ListAutomationsParams, opts ...RequestOption) (*AutomationList, error) {
	return callFor[AutomationList](ctx, s.c, "listAutomations", request{params: params}, requestOptions(opts))
}

// ListAll walks every Automation of every page, starting at the first page
// on each range (a Cursor in params is ignored, params is not modified).
// Each page is its own call with ctx and opts; an error ends the walk.
func (s *AutomationsService) ListAll(ctx context.Context, params *ListAutomationsParams, opts ...RequestOption) iter.Seq2[Automation, error] {
	return paginate(func(cursor *string) ([]Automation, *string, error) {
		var p ListAutomationsParams
		if params != nil {
			p = *params
		}
		p.Cursor = cursor
		page, err := s.List(ctx, &p, opts...)
		if err != nil {
			return nil, nil, err
		}
		return page.Data, page.NextCursor, nil
	})
}

// Get returns an automation with its run counts.
func (s *AutomationsService) Get(ctx context.Context, id string, opts ...RequestOption) (*AutomationWithCounts, error) {
	return callFor[AutomationWithCounts](ctx, s.c, "getAutomation", request{pathParams: []string{id}}, requestOptions(opts))
}

// Update changes an automation's status.
func (s *AutomationsService) Update(ctx context.Context, id string, body *UpdateAutomationRequest, opts ...RequestOption) (*Automation, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[Automation](ctx, s.c, "updateAutomation", request{pathParams: []string{id}, body: b}, requestOptions(opts))
}

// ListRuns returns one page of an automation's runs.
func (s *AutomationsService) ListRuns(ctx context.Context, id string, params *ListAutomationRunsParams, opts ...RequestOption) (*AutomationRunList, error) {
	return callFor[AutomationRunList](ctx, s.c, "listAutomationRuns", request{pathParams: []string{id}, params: params}, requestOptions(opts))
}

// ListAllRuns walks every AutomationRun of every page, starting at the first page
// on each range (a Cursor in params is ignored, params is not modified).
// Each page is its own call with ctx and opts; an error ends the walk.
func (s *AutomationsService) ListAllRuns(ctx context.Context, id string, params *ListAutomationRunsParams, opts ...RequestOption) iter.Seq2[AutomationRun, error] {
	return paginate(func(cursor *string) ([]AutomationRun, *string, error) {
		var p ListAutomationRunsParams
		if params != nil {
			p = *params
		}
		p.Cursor = cursor
		page, err := s.ListRuns(ctx, id, &p, opts...)
		if err != nil {
			return nil, nil, err
		}
		return page.Data, page.NextCursor, nil
	})
}

// AutomationRunsService holds the automation runs operations; use it through Client.AutomationRuns.
type AutomationRunsService struct{ c *core }

// String names the service only.
func (s *AutomationRunsService) String() string { return "emailplatform.AutomationRunsService" }

// Get returns an automation run with its emails.
func (s *AutomationRunsService) Get(ctx context.Context, id string, opts ...RequestOption) (*AutomationRunWithEmails, error) {
	return callFor[AutomationRunWithEmails](ctx, s.c, "getAutomationRun", request{pathParams: []string{id}}, requestOptions(opts))
}

// Cancel cancels an active automation run.
func (s *AutomationRunsService) Cancel(ctx context.Context, id string, opts ...RequestOption) (*AutomationRun, error) {
	return callFor[AutomationRun](ctx, s.c, "cancelAutomationRun", request{pathParams: []string{id}}, requestOptions(opts))
}

// ContactEventsService holds the contact events operations; use it through Client.ContactEvents.
type ContactEventsService struct{ c *core }

// String names the service only.
func (s *ContactEventsService) String() string { return "emailplatform.ContactEventsService" }

// Create records a contact event, which may start automation runs. Retries reuse one Idempotency-Key.
func (s *ContactEventsService) Create(ctx context.Context, body *CreateContactEventRequest, opts ...IdempotentOption) (*ContactEvent, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[ContactEvent](ctx, s.c, "createContactEvent", request{body: b}, idempotentOptions(opts))
}

// UsageService holds the usage operations; use it through Client.Usage.
type UsageService struct{ c *core }

// String names the service only.
func (s *UsageService) String() string { return "emailplatform.UsageService" }

// Get returns the project's daily usage.
func (s *UsageService) Get(ctx context.Context, params *GetUsageParams, opts ...RequestOption) (*Usage, error) {
	return callFor[Usage](ctx, s.c, "getUsage", request{params: params}, requestOptions(opts))
}

// TemplatesService holds the templates operations; use it through Client.Templates.
type TemplatesService struct{ c *core }

// String names the service only.
func (s *TemplatesService) String() string { return "emailplatform.TemplatesService" }

// Create creates a template.
func (s *TemplatesService) Create(ctx context.Context, body *CreateTemplateRequest, opts ...RequestOption) (*Template, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[Template](ctx, s.c, "createTemplate", request{body: b}, requestOptions(opts))
}

// Get returns a template.
func (s *TemplatesService) Get(ctx context.Context, id string, opts ...RequestOption) (*Template, error) {
	return callFor[Template](ctx, s.c, "getTemplate", request{pathParams: []string{id}}, requestOptions(opts))
}

// PublishVersion publishes a new, immutable version of a template.
func (s *TemplatesService) PublishVersion(ctx context.Context, id string, body *PublishTemplateVersionRequest, opts ...RequestOption) (*TemplateVersionCreated, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[TemplateVersionCreated](ctx, s.c, "publishTemplateVersion", request{pathParams: []string{id}, body: b}, requestOptions(opts))
}

// GetVersion returns one version of a template.
func (s *TemplatesService) GetVersion(ctx context.Context, id string, version int, opts ...RequestOption) (*TemplateVersion, error) {
	v, err := versionParam(version)
	if err != nil {
		return nil, err
	}
	return callFor[TemplateVersion](ctx, s.c, "getTemplateVersion", request{pathParams: []string{id, v}}, requestOptions(opts))
}

// WebhookEndpointsService holds the webhook endpoints operations; use it through Client.WebhookEndpoints.
type WebhookEndpointsService struct{ c *core }

// String names the service only.
func (s *WebhookEndpointsService) String() string { return "emailplatform.WebhookEndpointsService" }

// Create registers a webhook endpoint and returns its signing secret once. A 503 or timeout leaves the outcome unknown and is not retried.
func (s *WebhookEndpointsService) Create(ctx context.Context, body *CreateWebhookEndpointRequest, opts ...RequestOption) (*WebhookEndpointCreated, error) {
	b, err := requiredBody(body)
	if err != nil {
		return nil, err
	}
	return callFor[WebhookEndpointCreated](ctx, s.c, "createWebhookEndpoint", request{body: b}, requestOptions(opts))
}
