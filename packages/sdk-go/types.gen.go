// Code generated from api/openapi.yaml by oapi-codegen; DO NOT EDIT.
// Regenerate with `make sdk-go-generate`.

package emailplatform

import (
	"encoding/json"
	"time"
)

// Defines values for AttributeConditionStepOp.
const (
	Eq        AttributeConditionStepOp = "eq"
	Exists    AttributeConditionStepOp = "exists"
	Neq       AttributeConditionStepOp = "neq"
	NotExists AttributeConditionStepOp = "not_exists"
)

// Valid indicates whether the value is a known member of the AttributeConditionStepOp enum.
func (e AttributeConditionStepOp) Valid() bool {
	switch e {
	case Eq:
		return true
	case Exists:
		return true
	case Neq:
		return true
	case NotExists:
		return true
	default:
		return false
	}
}

// Defines values for AutomationRunReason.
const (
	AutomationRunReasonAutomationArchived AutomationRunReason = "automation_archived"
	AutomationRunReasonCanceled           AutomationRunReason = "canceled"
	AutomationRunReasonConditionNotMet    AutomationRunReason = "condition_not_met"
	AutomationRunReasonInternalError      AutomationRunReason = "internal_error"
	AutomationRunReasonProjectDisabled    AutomationRunReason = "project_disabled"
	AutomationRunReasonRenderFailed       AutomationRunReason = "render_failed"
	AutomationRunReasonSuppressed         AutomationRunReason = "suppressed"
	AutomationRunReasonUnsubscribed       AutomationRunReason = "unsubscribed"
)

// Valid indicates whether the value is a known member of the AutomationRunReason enum.
func (e AutomationRunReason) Valid() bool {
	switch e {
	case AutomationRunReasonAutomationArchived:
		return true
	case AutomationRunReasonCanceled:
		return true
	case AutomationRunReasonConditionNotMet:
		return true
	case AutomationRunReasonInternalError:
		return true
	case AutomationRunReasonProjectDisabled:
		return true
	case AutomationRunReasonRenderFailed:
		return true
	case AutomationRunReasonSuppressed:
		return true
	case AutomationRunReasonUnsubscribed:
		return true
	default:
		return false
	}
}

// Defines values for AutomationRunStatus.
const (
	AutomationRunStatusActive    AutomationRunStatus = "active"
	AutomationRunStatusCanceled  AutomationRunStatus = "canceled"
	AutomationRunStatusCompleted AutomationRunStatus = "completed"
	AutomationRunStatusExited    AutomationRunStatus = "exited"
	AutomationRunStatusFailed    AutomationRunStatus = "failed"
)

// Valid indicates whether the value is a known member of the AutomationRunStatus enum.
func (e AutomationRunStatus) Valid() bool {
	switch e {
	case AutomationRunStatusActive:
		return true
	case AutomationRunStatusCanceled:
		return true
	case AutomationRunStatusCompleted:
		return true
	case AutomationRunStatusExited:
		return true
	case AutomationRunStatusFailed:
		return true
	default:
		return false
	}
}

// Defines values for AutomationStatus.
const (
	AutomationStatusActive   AutomationStatus = "active"
	AutomationStatusArchived AutomationStatus = "archived"
	AutomationStatusPaused   AutomationStatus = "paused"
)

// Valid indicates whether the value is a known member of the AutomationStatus enum.
func (e AutomationStatus) Valid() bool {
	switch e {
	case AutomationStatusActive:
		return true
	case AutomationStatusArchived:
		return true
	case AutomationStatusPaused:
		return true
	default:
		return false
	}
}

// Defines values for BroadcastStatus.
const (
	BroadcastStatusCanceled  BroadcastStatus = "canceled"
	BroadcastStatusCompleted BroadcastStatus = "completed"
	BroadcastStatusDraft     BroadcastStatus = "draft"
	BroadcastStatusScheduled BroadcastStatus = "scheduled"
	BroadcastStatusSending   BroadcastStatus = "sending"
)

// Valid indicates whether the value is a known member of the BroadcastStatus enum.
func (e BroadcastStatus) Valid() bool {
	switch e {
	case BroadcastStatusCanceled:
		return true
	case BroadcastStatusCompleted:
		return true
	case BroadcastStatusDraft:
		return true
	case BroadcastStatusScheduled:
		return true
	case BroadcastStatusSending:
		return true
	default:
		return false
	}
}

// Defines values for ContactStatus.
const (
	ContactStatusSubscribed   ContactStatus = "subscribed"
	ContactStatusUnsubscribed ContactStatus = "unsubscribed"
)

// Valid indicates whether the value is a known member of the ContactStatus enum.
func (e ContactStatus) Valid() bool {
	switch e {
	case ContactStatusSubscribed:
		return true
	case ContactStatusUnsubscribed:
		return true
	default:
		return false
	}
}

// Defines values for CreateAutomationRequestStatus.
const (
	CreateAutomationRequestStatusActive CreateAutomationRequestStatus = "active"
	CreateAutomationRequestStatusPaused CreateAutomationRequestStatus = "paused"
)

// Valid indicates whether the value is a known member of the CreateAutomationRequestStatus enum.
func (e CreateAutomationRequestStatus) Valid() bool {
	switch e {
	case CreateAutomationRequestStatusActive:
		return true
	case CreateAutomationRequestStatusPaused:
		return true
	default:
		return false
	}
}

// Defines values for CreateEmailResponseStatus.
const (
	CreateEmailResponseStatusQueued CreateEmailResponseStatus = "queued"
)

// Valid indicates whether the value is a known member of the CreateEmailResponseStatus enum.
func (e CreateEmailResponseStatus) Valid() bool {
	switch e {
	case CreateEmailResponseStatusQueued:
		return true
	default:
		return false
	}
}

// Defines values for EmailEventOrigin.
const (
	Platform EmailEventOrigin = "platform"
	Ses      EmailEventOrigin = "ses"
)

// Valid indicates whether the value is a known member of the EmailEventOrigin enum.
func (e EmailEventOrigin) Valid() bool {
	switch e {
	case Platform:
		return true
	case Ses:
		return true
	default:
		return false
	}
}

// Defines values for EmailEventType.
const (
	EmailEventTypeBounced         EmailEventType = "bounced"
	EmailEventTypeCanceled        EmailEventType = "canceled"
	EmailEventTypeCaptured        EmailEventType = "captured"
	EmailEventTypeComplained      EmailEventType = "complained"
	EmailEventTypeDelivered       EmailEventType = "delivered"
	EmailEventTypeDeliveryDelayed EmailEventType = "delivery_delayed"
	EmailEventTypeFailed          EmailEventType = "failed"
	EmailEventTypeRejected        EmailEventType = "rejected"
	EmailEventTypeSend            EmailEventType = "send"
	EmailEventTypeSubmitted       EmailEventType = "submitted"
)

// Valid indicates whether the value is a known member of the EmailEventType enum.
func (e EmailEventType) Valid() bool {
	switch e {
	case EmailEventTypeBounced:
		return true
	case EmailEventTypeCanceled:
		return true
	case EmailEventTypeCaptured:
		return true
	case EmailEventTypeComplained:
		return true
	case EmailEventTypeDelivered:
		return true
	case EmailEventTypeDeliveryDelayed:
		return true
	case EmailEventTypeFailed:
		return true
	case EmailEventTypeRejected:
		return true
	case EmailEventTypeSend:
		return true
	case EmailEventTypeSubmitted:
		return true
	default:
		return false
	}
}

// Defines values for EmailStatus.
const (
	EmailStatusBounced    EmailStatus = "bounced"
	EmailStatusCanceled   EmailStatus = "canceled"
	EmailStatusCaptured   EmailStatus = "captured"
	EmailStatusComplained EmailStatus = "complained"
	EmailStatusDelivered  EmailStatus = "delivered"
	EmailStatusFailed     EmailStatus = "failed"
	EmailStatusQueued     EmailStatus = "queued"
	EmailStatusSending    EmailStatus = "sending"
	EmailStatusSubmitted  EmailStatus = "submitted"
)

// Valid indicates whether the value is a known member of the EmailStatus enum.
func (e EmailStatus) Valid() bool {
	switch e {
	case EmailStatusBounced:
		return true
	case EmailStatusCanceled:
		return true
	case EmailStatusCaptured:
		return true
	case EmailStatusComplained:
		return true
	case EmailStatusDelivered:
		return true
	case EmailStatusFailed:
		return true
	case EmailStatusQueued:
		return true
	case EmailStatusSending:
		return true
	case EmailStatusSubmitted:
		return true
	default:
		return false
	}
}

// Defines values for ErrorCode.
const (
	ErrorCodeAutomationCanceled    ErrorCode = "automation_canceled"
	ErrorCodeBroadcastCanceled     ErrorCode = "broadcast_canceled"
	ErrorCodeContactDeleted        ErrorCode = "contact_deleted"
	ErrorCodeContentMissing        ErrorCode = "content_missing"
	ErrorCodeExpired               ErrorCode = "expired"
	ErrorCodeInternalError         ErrorCode = "internal_error"
	ErrorCodeMessageInvalid        ErrorCode = "message_invalid"
	ErrorCodeProjectDisabled       ErrorCode = "project_disabled"
	ErrorCodeProviderAuthFailed    ErrorCode = "provider_auth_failed"
	ErrorCodeProviderDeferred      ErrorCode = "provider_deferred"
	ErrorCodeProviderProtocolError ErrorCode = "provider_protocol_error"
	ErrorCodeProviderRejected      ErrorCode = "provider_rejected"
	ErrorCodeProviderThrottled     ErrorCode = "provider_throttled"
	ErrorCodeProviderTimeout       ErrorCode = "provider_timeout"
	ErrorCodeProviderUnreachable   ErrorCode = "provider_unreachable"
	ErrorCodeRenderFailed          ErrorCode = "render_failed"
	ErrorCodeSenderNotVerified     ErrorCode = "sender_not_verified"
	ErrorCodeSuppressed            ErrorCode = "suppressed"
	ErrorCodeUnsafeTarget          ErrorCode = "unsafe_target"
	ErrorCodeUnsubscribed          ErrorCode = "unsubscribed"
)

// Valid indicates whether the value is a known member of the ErrorCode enum.
func (e ErrorCode) Valid() bool {
	switch e {
	case ErrorCodeAutomationCanceled:
		return true
	case ErrorCodeBroadcastCanceled:
		return true
	case ErrorCodeContactDeleted:
		return true
	case ErrorCodeContentMissing:
		return true
	case ErrorCodeExpired:
		return true
	case ErrorCodeInternalError:
		return true
	case ErrorCodeMessageInvalid:
		return true
	case ErrorCodeProjectDisabled:
		return true
	case ErrorCodeProviderAuthFailed:
		return true
	case ErrorCodeProviderDeferred:
		return true
	case ErrorCodeProviderProtocolError:
		return true
	case ErrorCodeProviderRejected:
		return true
	case ErrorCodeProviderThrottled:
		return true
	case ErrorCodeProviderTimeout:
		return true
	case ErrorCodeProviderUnreachable:
		return true
	case ErrorCodeRenderFailed:
		return true
	case ErrorCodeSenderNotVerified:
		return true
	case ErrorCodeSuppressed:
		return true
	case ErrorCodeUnsafeTarget:
		return true
	case ErrorCodeUnsubscribed:
		return true
	default:
		return false
	}
}

// Defines values for EventConditionStepOp.
const (
	NotOccurred EventConditionStepOp = "not_occurred"
	Occurred    EventConditionStepOp = "occurred"
)

// Valid indicates whether the value is a known member of the EventConditionStepOp enum.
func (e EventConditionStepOp) Valid() bool {
	switch e {
	case NotOccurred:
		return true
	case Occurred:
		return true
	default:
		return false
	}
}

// Defines values for UnsubscribeResultStatus.
const (
	UnsubscribeResultStatusUnsubscribed UnsubscribeResultStatus = "unsubscribed"
)

// Valid indicates whether the value is a known member of the UnsubscribeResultStatus enum.
func (e UnsubscribeResultStatus) Valid() bool {
	switch e {
	case UnsubscribeResultStatusUnsubscribed:
		return true
	default:
		return false
	}
}

// Defines values for VariableSpecType.
const (
	Boolean VariableSpecType = "boolean"
	Number  VariableSpecType = "number"
	String  VariableSpecType = "string"
)

// Valid indicates whether the value is a known member of the VariableSpecType enum.
func (e VariableSpecType) Valid() bool {
	switch e {
	case Boolean:
		return true
	case Number:
		return true
	case String:
		return true
	default:
		return false
	}
}

// Defines values for WebhookEndpointCreatedStatus.
const (
	WebhookEndpointCreatedStatusActive WebhookEndpointCreatedStatus = "active"
)

// Valid indicates whether the value is a known member of the WebhookEndpointCreatedStatus enum.
func (e WebhookEndpointCreatedStatus) Valid() bool {
	switch e {
	case WebhookEndpointCreatedStatusActive:
		return true
	default:
		return false
	}
}

// Defines values for WebhookEventType.
const (
	EmailBounced    WebhookEventType = "email.bounced"
	EmailCanceled   WebhookEventType = "email.canceled"
	EmailCaptured   WebhookEventType = "email.captured"
	EmailComplained WebhookEventType = "email.complained"
	EmailDelivered  WebhookEventType = "email.delivered"
	EmailFailed     WebhookEventType = "email.failed"
	EmailSubmitted  WebhookEventType = "email.submitted"
)

// Valid indicates whether the value is a known member of the WebhookEventType enum.
func (e WebhookEventType) Valid() bool {
	switch e {
	case EmailBounced:
		return true
	case EmailCanceled:
		return true
	case EmailCaptured:
		return true
	case EmailComplained:
		return true
	case EmailDelivered:
		return true
	case EmailFailed:
		return true
	case EmailSubmitted:
		return true
	default:
		return false
	}
}

// AddAudienceContactsRequest defines model for AddAudienceContactsRequest.
type AddAudienceContactsRequest struct {
	ContactIds []string `json:"contact_ids"`
}

// AddAudienceContactsResult defines model for AddAudienceContactsResult.
type AddAudienceContactsResult struct {
	Added          int `json:"added"`
	AlreadyMembers int `json:"already_members"`
	NotFound       int `json:"not_found"`
}

// AttributeConditionStep Compares the contact's current attribute. `eq`/`neq` need `value` of
// the same JSON type (`"1"` is not `1`; numbers compare numerically); a
// missing attribute fails `eq` and passes `neq`. `exists`/`not_exists`
// take no `value`.
type AttributeConditionStep struct {
	Attribute string                   `json:"attribute"`
	Op        AttributeConditionStepOp `json:"op"`
	Type      interface{}              `json:"type"`
	Value     *any                     `json:"value,omitempty"`
}

// AttributeConditionStepOp defines model for AttributeConditionStep.Op.
type AttributeConditionStepOp string

// Audience defines model for Audience.
type Audience struct {
	CreatedAt time.Time `json:"created_at"`
	Id        string    `json:"id"`
	Name      string    `json:"name"`
}

// AudienceList defines model for AudienceList.
type AudienceList struct {
	Data       []Audience `json:"data"`
	NextCursor *string    `json:"next_cursor"`
}

// AudienceWithCount defines model for AudienceWithCount.
type AudienceWithCount struct {
	ContactCount int       `json:"contact_count"`
	CreatedAt    time.Time `json:"created_at"`
	Id           string    `json:"id"`
	Name         string    `json:"name"`
}

// Automation defines model for Automation.
type Automation struct {
	AllowReentry bool      `json:"allow_reentry"`
	CreatedAt    time.Time `json:"created_at"`
	Id           string    `json:"id"`
	Name         string    `json:"name"`

	// Status `active`: starts runs. `paused`: starts no new runs; runs in flight continue. `archived`: final; all runs cancelled.
	Status    AutomationStatus  `json:"status"`
	Steps     []AutomationStep  `json:"steps"`
	Trigger   AutomationTrigger `json:"trigger"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// AutomationList defines model for AutomationList.
type AutomationList struct {
	Data       []Automation `json:"data"`
	NextCursor *string      `json:"next_cursor"`
}

// AutomationRun defines model for AutomationRun.
type AutomationRun struct {
	AutomationId string     `json:"automation_id"`
	ContactId    string     `json:"contact_id"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	Id           string     `json:"id"`

	// NextStepAt When the next step is due; `null` once the run ended.
	NextStepAt *time.Time `json:"next_step_at"`

	// Reason Why a run ended; `null` for `active` and `completed`.
	Reason *AutomationRunReason `json:"reason"`

	// Status `active` (waiting for its next step) → `completed` (all steps done),
	// `exited` (a condition did not hold, the contact unsubscribed, the
	// address is suppressed or the project was disabled), `canceled` (by
	// `POST /v1/automation-runs/{id}/cancel` or by archiving the
	// automation) or `failed` (the email could not be rendered, or an
	// internal error persisted). All but `active` are final.
	Status AutomationRunStatus `json:"status"`

	// Step Index of the next step to run (equals the number of steps once completed).
	Step      int       `json:"step"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AutomationRunList defines model for AutomationRunList.
type AutomationRunList struct {
	Data       []AutomationRun `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}

// AutomationRunReason Why a run ended; `null` for `active` and `completed`.
type AutomationRunReason string

// AutomationRunStatus `active` (waiting for its next step) → `completed` (all steps done),
// `exited` (a condition did not hold, the contact unsubscribed, the
// address is suppressed or the project was disabled), `canceled` (by
// `POST /v1/automation-runs/{id}/cancel` or by archiving the
// automation) or `failed` (the email could not be rendered, or an
// internal error persisted). All but `active` are final.
type AutomationRunStatus string

// AutomationRunWithEmails defines model for AutomationRunWithEmails.
type AutomationRunWithEmails struct {
	AutomationId string    `json:"automation_id"`
	ContactId    string    `json:"contact_id"`
	CreatedAt    time.Time `json:"created_at"`
	Emails       []struct {
		Id string `json:"id"`

		// Status `queued` → `sending` → `captured` (local test sink), `submitted`
		// (real provider) or `failed`; a `queued` email can be cancelled
		// (`canceled`, terminal, never handed to a provider). A scheduled
		// email stays `queued` until it is due. After `submitted`, provider events move
		// the status to `delivered`, `bounced` or `complained`; a later event
		// never moves it back (`complained` > `bounced` > `delivered` >
		// `submitted`).
		Status EmailStatus `json:"status"`
		Step   int         `json:"step"`
	} `json:"emails"`
	FinishedAt *time.Time `json:"finished_at"`
	Id         string     `json:"id"`

	// NextStepAt When the next step is due; `null` once the run ended.
	NextStepAt *time.Time `json:"next_step_at"`

	// Reason Why a run ended; `null` for `active` and `completed`.
	Reason *AutomationRunReason `json:"reason"`

	// Status `active` (waiting for its next step) → `completed` (all steps done),
	// `exited` (a condition did not hold, the contact unsubscribed, the
	// address is suppressed or the project was disabled), `canceled` (by
	// `POST /v1/automation-runs/{id}/cancel` or by archiving the
	// automation) or `failed` (the email could not be rendered, or an
	// internal error persisted). All but `active` are final.
	Status AutomationRunStatus `json:"status"`

	// Step Index of the next step to run (equals the number of steps once completed).
	Step      int       `json:"step"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AutomationStatus `active`: starts runs. `paused`: starts no new runs; runs in flight continue. `archived`: final; all runs cancelled.
type AutomationStatus string

// AutomationStep One step; `type` selects the shape.
type AutomationStep = json.RawMessage

// AutomationTrigger defines model for AutomationTrigger.
type AutomationTrigger struct {
	// Event Event name, e.g. `user.signed_up`.
	Event EventName `json:"event"`
}

// AutomationWithCounts defines model for AutomationWithCounts.
type AutomationWithCounts struct {
	AllowReentry bool      `json:"allow_reentry"`
	CreatedAt    time.Time `json:"created_at"`
	Id           string    `json:"id"`
	Name         string    `json:"name"`
	RunCounts    struct {
		Active    int `json:"active"`
		Canceled  int `json:"canceled"`
		Completed int `json:"completed"`
		Exited    int `json:"exited"`
		Failed    int `json:"failed"`
	} `json:"run_counts"`

	// Status `active`: starts runs. `paused`: starts no new runs; runs in flight continue. `archived`: final; all runs cancelled.
	Status    AutomationStatus  `json:"status"`
	Steps     []AutomationStep  `json:"steps"`
	Trigger   AutomationTrigger `json:"trigger"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Broadcast defines model for Broadcast.
type Broadcast struct {
	AudienceId string    `json:"audience_id"`
	CreatedAt  time.Time `json:"created_at"`

	// FinishedAt When the last recipient email was created, or when the broadcast was cancelled.
	FinishedAt  *time.Time `json:"finished_at"`
	From        string     `json:"from"`
	Id          string     `json:"id"`
	Name        string     `json:"name"`
	ScheduledAt *time.Time `json:"scheduled_at"`
	StartedAt   *time.Time `json:"started_at"`

	// Status `draft` → `scheduled` → `sending` (recipient emails are being
	// created) → `completed` (all recipient emails exist; delivery is
	// tracked per email). `canceled` from any other status.
	Status          BroadcastStatus `json:"status"`
	TemplateId      string          `json:"template_id"`
	TemplateVersion int             `json:"template_version"`
	UnsubscribeUrl  string          `json:"unsubscribe_url"`
}

// BroadcastList defines model for BroadcastList.
type BroadcastList struct {
	Data       []Broadcast `json:"data"`
	NextCursor *string     `json:"next_cursor"`
}

// BroadcastStatus `draft` → `scheduled` → `sending` (recipient emails are being
// created) → `completed` (all recipient emails exist; delivery is
// tracked per email). `canceled` from any other status.
type BroadcastStatus string

// BroadcastWithCounts defines model for BroadcastWithCounts.
type BroadcastWithCounts struct {
	AudienceId string `json:"audience_id"`
	Counts     struct {
		// ByStatus Current status of the recipient emails (emails deleted by retention are no longer counted).
		ByStatus struct {
			Bounced    int `json:"bounced"`
			Canceled   int `json:"canceled"`
			Captured   int `json:"captured"`
			Complained int `json:"complained"`
			Delivered  int `json:"delivered"`
			Failed     int `json:"failed"`
			Queued     int `json:"queued"`
			Sending    int `json:"sending"`
			Submitted  int `json:"submitted"`
		} `json:"by_status"`

		// Recipients Recipient emails created.
		Recipients int `json:"recipients"`

		// SkippedInvalid Members whose attributes did not render with the version.
		SkippedInvalid      int `json:"skipped_invalid"`
		SkippedSuppressed   int `json:"skipped_suppressed"`
		SkippedUnsubscribed int `json:"skipped_unsubscribed"`
	} `json:"counts"`
	CreatedAt time.Time `json:"created_at"`

	// FinishedAt When the last recipient email was created, or when the broadcast was cancelled.
	FinishedAt  *time.Time `json:"finished_at"`
	From        string     `json:"from"`
	Id          string     `json:"id"`
	Name        string     `json:"name"`
	ScheduledAt *time.Time `json:"scheduled_at"`
	StartedAt   *time.Time `json:"started_at"`

	// Status `draft` → `scheduled` → `sending` (recipient emails are being
	// created) → `completed` (all recipient emails exist; delivery is
	// tracked per email). `canceled` from any other status.
	Status          BroadcastStatus `json:"status"`
	TemplateId      string          `json:"template_id"`
	TemplateVersion int             `json:"template_version"`
	UnsubscribeUrl  string          `json:"unsubscribe_url"`
}

// Contact defines model for Contact.
type Contact struct {
	// Attributes Values for personalising broadcasts: at most 20 entries, names
	// `^[a-z][a-z0-9_]{0,39}$` (not `unsubscribe_url`), values strings (at
	// most 1000 characters), finite numbers or booleans; at most 8 KiB as
	// compact JSON. Attributes are matched by name to a template's
	// declared variables; undeclared ones are ignored. Never logged.
	Attributes    ContactAttributes `json:"attributes"`
	ConsentSource *string           `json:"consent_source"`
	CreatedAt     time.Time         `json:"created_at"`
	Email         string            `json:"email"`
	Id            string            `json:"id"`

	// Status `subscribed`: the person consented to broadcasts. `unsubscribed`: no broadcast is sent to the contact.
	Status          ContactStatus `json:"status"`
	StatusChangedAt time.Time     `json:"status_changed_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// ContactAttributes Values for personalising broadcasts: at most 20 entries, names
// `^[a-z][a-z0-9_]{0,39}$` (not `unsubscribe_url`), values strings (at
// most 1000 characters), finite numbers or booleans; at most 8 KiB as
// compact JSON. Attributes are matched by name to a template's
// declared variables; undeclared ones are ignored. Never logged.
type ContactAttributes map[string]any

// ContactEvent defines model for ContactEvent.
type ContactEvent struct {
	ContactId string    `json:"contact_id"`
	CreatedAt time.Time `json:"created_at"`
	Id        string    `json:"id"`

	// Name Event name, e.g. `user.signed_up`.
	Name EventName `json:"name"`

	// Runs Runs this event started (empty when no active automation has this trigger or re-entry rules prevented it).
	Runs []struct {
		AutomationId string `json:"automation_id"`
		Id           string `json:"id"`
	} `json:"runs"`
}

// ContactList defines model for ContactList.
type ContactList struct {
	Data []Contact `json:"data"`

	// NextCursor Pass as `cursor` for the next page; `null` on the last page.
	NextCursor *string `json:"next_cursor"`
}

// ContactStatus `subscribed`: the person consented to broadcasts. `unsubscribed`: no broadcast is sent to the contact.
type ContactStatus string

// CreateAudienceRequest defines model for CreateAudienceRequest.
type CreateAudienceRequest struct {
	Name string `json:"name"`
}

// CreateAutomationRequest defines model for CreateAutomationRequest.
type CreateAutomationRequest struct {
	AllowReentry *bool                          `json:"allow_reentry,omitempty"`
	Name         string                         `json:"name"`
	Status       *CreateAutomationRequestStatus `json:"status,omitempty"`

	// Steps At least one `send` step; at most 64 KiB as stored.
	Steps   []AutomationStep  `json:"steps"`
	Trigger AutomationTrigger `json:"trigger"`
}

// CreateAutomationRequestStatus defines model for CreateAutomationRequest.Status.
type CreateAutomationRequestStatus string

// CreateBroadcastRequest defines model for CreateBroadcastRequest.
type CreateBroadcastRequest struct {
	AudienceId      string          `json:"audience_id"`
	From            string          `json:"from"`
	Name            *string         `json:"name,omitempty"`
	TemplateId      string          `json:"template_id"`
	TemplateVersion json.RawMessage `json:"template_version"`

	// UnsubscribeUrl Absolute `https` URL of the app's unsubscribe page containing `{token}` exactly once, e.g. `https://app.example.test/unsubscribe?t={token}`.
	UnsubscribeUrl string `json:"unsubscribe_url"`

	// Variables Shared values for declared variables (at most 16 KiB); contact attributes override them. Not echoed.
	Variables *map[string]any `json:"variables,omitempty"`
}

// CreateContactEventRequest Exactly one of `contact_id` and `email`.
type CreateContactEventRequest struct {
	ContactId *string `json:"contact_id,omitempty"`

	// Data Values for personalising broadcasts: at most 20 entries, names
	// `^[a-z][a-z0-9_]{0,39}$` (not `unsubscribe_url`), values strings (at
	// most 1000 characters), finite numbers or booleans; at most 8 KiB as
	// compact JSON. Attributes are matched by name to a template's
	// declared variables; undeclared ones are ignored. Never logged.
	Data  *ContactAttributes `json:"data,omitempty"`
	Email *string            `json:"email,omitempty"`

	// Name Event name, e.g. `user.signed_up`.
	Name EventName `json:"name"`
}

// CreateContactRequest defines model for CreateContactRequest.
type CreateContactRequest struct {
	// Attributes Values for personalising broadcasts: at most 20 entries, names
	// `^[a-z][a-z0-9_]{0,39}$` (not `unsubscribe_url`), values strings (at
	// most 1000 characters), finite numbers or booleans; at most 8 KiB as
	// compact JSON. Attributes are matched by name to a template's
	// declared variables; undeclared ones are ignored. Never logged.
	Attributes *ContactAttributes `json:"attributes,omitempty"`

	// ConsentSource Where the consent came from (printable characters). Required when `status` is `subscribed`.
	ConsentSource *string `json:"consent_source,omitempty"`

	// Email Bare address, ASCII only (as for `to`).
	Email string `json:"email"`

	// Status `subscribed`: the person consented to broadcasts. `unsubscribed`: no broadcast is sent to the contact.
	Status ContactStatus `json:"status"`
}

// CreateEmailRequest Raw form: `subject` plus `text` and/or `html`. Template form:
// `template_id` and `template_version`, plus `variables` (may be
// omitted when the version has no required variables). Mixing the
// forms gives `422 validation_failed`.
type CreateEmailRequest struct {
	// From Bare address, no display name, ASCII only (international domains
	// in punycode, e.g. `xn--bcher-kva.test`).
	From string `json:"from"`

	// Html HTML body, at most 512 KiB. At least one of text/html is required.
	Html *string `json:"html,omitempty"`

	// ScheduledAt Send not before this time (RFC 3339), at most 30 days ahead by
	// default (`422 validation_failed` otherwise). A time at or before
	// now sends immediately (stored as `null`). A project may have at
	// most 10000 planned sends waiting by default
	// (`422 schedule_limit_reached`). Part of the idempotency
	// fingerprint when present.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`

	// Subject Raw form only. Must not contain CR, LF or NUL.
	Subject *string `json:"subject,omitempty"`

	// TemplateId Template form. A template of the authenticated project.
	TemplateId *string `json:"template_id,omitempty"`

	// TemplateVersion Template form. A version number, or `"published"` for the highest version at this moment.
	TemplateVersion *json.RawMessage `json:"template_version,omitempty"`

	// Text Plain-text body, at most 512 KiB. At least one of text/html is required.
	Text *string `json:"text,omitempty"`

	// To Exactly one bare recipient address, ASCII only (international
	// domains in punycode). Other addresses get `422 validation_failed`.
	To string `json:"to"`

	// Variables Template form. Values for the version's declared variables:
	// strings, numbers or booleans as declared (`null` is not
	// accepted). Unknown names, missing required variables, wrong
	// types and strings over their `max_length` give
	// `422 variables_invalid`. Variables are not stored.
	Variables *map[string]any `json:"variables,omitempty"`
}

// CreateEmailResponse defines model for CreateEmailResponse.
type CreateEmailResponse struct {
	Id     string                    `json:"id"`
	Status CreateEmailResponseStatus `json:"status"`
}

// CreateEmailResponseStatus defines model for CreateEmailResponse.Status.
type CreateEmailResponseStatus string

// CreateTemplateRequest defines model for CreateTemplateRequest.
type CreateTemplateRequest struct {
	Name *string `json:"name,omitempty"`
	Slug string  `json:"slug"`
}

// CreateWebhookEndpointRequest defines model for CreateWebhookEndpointRequest.
type CreateWebhookEndpointRequest struct {
	EventTypes *[]WebhookEventType `json:"event_types,omitempty"`
	Url        string              `json:"url"`
}

// Email defines model for Email.
type Email struct {
	// Attempts Dispatch attempts started so far.
	Attempts int `json:"attempts"`

	// AutomationId The automation whose run sent this email; `null` otherwise.
	AutomationId *string `json:"automation_id"`

	// AutomationRunId The run that sent this email; `null` otherwise (also once the run was deleted with its contact or by retention).
	AutomationRunId *string `json:"automation_run_id"`

	// BroadcastId The broadcast this email belongs to; `null` otherwise.
	BroadcastId *string   `json:"broadcast_id"`
	CreatedAt   time.Time `json:"created_at"`

	// ErrorCode `null` when no attempt has failed or the email was dispatched.
	// While the status is `queued` or `sending`, the reason of the last
	// failed attempt (a retry may be pending; otherwise the email is
	// closed by expiry); with status `failed`, the
	// final reason.
	ErrorCode  *ErrorCode `json:"error_code"`
	FinishedAt *time.Time `json:"finished_at"`
	From       string     `json:"from"`
	Id         string     `json:"id"`

	// ScheduledAt Planned send time; `null` for immediate sends.
	ScheduledAt *time.Time `json:"scheduled_at"`

	// Status `queued` → `sending` → `captured` (local test sink), `submitted`
	// (real provider) or `failed`; a `queued` email can be cancelled
	// (`canceled`, terminal, never handed to a provider). A scheduled
	// email stays `queued` until it is due. After `submitted`, provider events move
	// the status to `delivered`, `bounced` or `complained`; a later event
	// never moves it back (`complained` > `bounced` > `delivered` >
	// `submitted`).
	Status  EmailStatus `json:"status"`
	Subject string      `json:"subject"`

	// TemplateId Template of a template send; `null` for raw sends.
	TemplateId *string `json:"template_id"`

	// TemplateVersion The resolved version number (also when `"published"` was requested).
	TemplateVersion *int      `json:"template_version"`
	To              string    `json:"to"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// EmailEvent defines model for EmailEvent.
type EmailEvent struct {
	// Details Non-personal details only: `error_code` (failed),
	// `bounce_type`/`bounce_subtype` (bounced),
	// `complaint_feedback_type` (complained), `delay_type`
	// (delivery_delayed). Never addresses or diagnostic text.
	Details    map[string]string `json:"details"`
	Id         string            `json:"id"`
	OccurredAt time.Time         `json:"occurred_at"`
	Origin     EmailEventOrigin  `json:"origin"`

	// Provider Provider involved (`mailpit`, `ses`); `null` for `failed` events.
	Provider   *string   `json:"provider"`
	ReceivedAt time.Time `json:"received_at"`

	// Type Platform events: `captured`, `submitted`, `failed`, `canceled`. Provider
	// events: `send`, `delivered`, `bounced`, `complained`, `rejected`,
	// `delivery_delayed`. Engagement (open/click) is not tracked.
	Type EmailEventType `json:"type"`
}

// EmailEventOrigin defines model for EmailEvent.Origin.
type EmailEventOrigin string

// EmailEventType Platform events: `captured`, `submitted`, `failed`, `canceled`. Provider
// events: `send`, `delivered`, `bounced`, `complained`, `rejected`,
// `delivery_delayed`. Engagement (open/click) is not tracked.
type EmailEventType string

// EmailEventList defines model for EmailEventList.
type EmailEventList struct {
	Data []EmailEvent `json:"data"`

	// NextCursor Pass as `cursor` for the next page; `null` on the last page.
	NextCursor *string `json:"next_cursor"`
}

// EmailList defines model for EmailList.
type EmailList struct {
	Data []Email `json:"data"`

	// NextCursor Pass as `cursor` for the next page; `null` on the last page.
	NextCursor *string `json:"next_cursor"`
}

// EmailStatus `queued` → `sending` → `captured` (local test sink), `submitted`
// (real provider) or `failed`; a `queued` email can be cancelled
// (`canceled`, terminal, never handed to a provider). A scheduled
// email stays `queued` until it is due. After `submitted`, provider events move
// the status to `delivered`, `bounced` or `complained`; a later event
// never moves it back (`complained` > `bounced` > `delivered` >
// `submitted`).
type EmailStatus string

// Error defines model for Error.
type Error struct {
	Error struct {
		// Code Stable snake_case error code.
		Code string `json:"code"`

		// Message Human-readable explanation; never contains internals.
		Message string `json:"message"`
	} `json:"error"`
}

// ErrorCode Why the last dispatch attempt failed. Retryable codes (the email is
// retried until its attempts are used up): `provider_unreachable`,
// `provider_timeout`, `provider_protocol_error`, `provider_deferred`,
// `provider_throttled`, `internal_error`. Permanent codes (no further
// attempts): `provider_rejected`, `provider_auth_failed`,
// `sender_not_verified`, `message_invalid`, `unsafe_target`,
// `suppressed`, `project_disabled`, `content_missing`, `expired`,
// and for broadcast emails `unsubscribed` (the contact unsubscribed
// before the send), `contact_deleted`, `broadcast_canceled` (the
// broadcast was cancelled after an attempt with unknown outcome),
// `render_failed` (the contact's attributes no longer render); for
// automation emails `unsubscribed`, `contact_deleted` and
// `automation_canceled` (the run was cancelled after an attempt with
// unknown outcome).
//
// `provider_timeout` and `provider_protocol_error` mean the outcome of
// that attempt is unknown: a `failed` email with one of these codes may
// still have been delivered. With a real provider, the same holds for
// `provider_unreachable` when the provider answered with a server error
// (5xx) rather than refusing the connection. The same holds for `suppressed` and
// `project_disabled` on an email with `attempts` > 0 whose earlier
// attempt ended with an unknown outcome: no further attempt is made.
type ErrorCode string

// EventConditionStep Checks whether an event of that name was reported for the contact after the run started (the trigger event itself does not count).
type EventConditionStep struct {
	// Event Event name, e.g. `user.signed_up`.
	Event EventName            `json:"event"`
	Op    EventConditionStepOp `json:"op"`
	Type  interface{}          `json:"type"`
}

// EventConditionStepOp defines model for EventConditionStep.Op.
type EventConditionStepOp string

// EventName Event name, e.g. `user.signed_up`.
type EventName = string

// PublishTemplateVersionRequest defines model for PublishTemplateVersionRequest.
type PublishTemplateVersionRequest struct {
	// Html HTML template, at most 512 KiB, rendered with contextual escaping.
	Html *string `json:"html,omitempty"`

	// Subject Plain-text template, at most 1000 bytes.
	Subject string `json:"subject"`

	// Text Plain-text template, at most 512 KiB.
	Text string `json:"text"`

	// Variables Declared variables by name (`^[a-z][a-z0-9_]{0,39}$`).
	Variables *VariableSchema `json:"variables,omitempty"`
}

// ScheduleBroadcastRequest defines model for ScheduleBroadcastRequest.
type ScheduleBroadcastRequest struct {
	// ScheduledAt Start time (RFC 3339), at most 30 days ahead by default; omitted or past = now.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

// SendStep defines model for SendStep.
type SendStep struct {
	From       string `json:"from"`
	TemplateId string `json:"template_id"`

	// TemplateVersion In requests a positive integer or `"published"` (resolved at create); in responses always the number.
	TemplateVersion json.RawMessage `json:"template_version"`
	Type            interface{}     `json:"type"`

	// UnsubscribeUrl Absolute `https` URL of the app's unsubscribe page containing `{token}` exactly once.
	UnsubscribeUrl string `json:"unsubscribe_url"`

	// Variables Shared values for declared variables (at most 16 KiB); contact attributes and event data override them.
	Variables *map[string]any `json:"variables,omitempty"`
}

// Template defines model for Template.
type Template struct {
	CreatedAt time.Time `json:"created_at"`
	Id        string    `json:"id"`

	// LatestVersion Highest published version; `null` while none exists.
	LatestVersion *int   `json:"latest_version"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
}

// TemplateVersion defines model for TemplateVersion.
type TemplateVersion struct {
	CreatedAt  time.Time `json:"created_at"`
	Html       *string   `json:"html"`
	Subject    string    `json:"subject"`
	TemplateId string    `json:"template_id"`
	Text       string    `json:"text"`

	// Variables Declared variables by name (`^[a-z][a-z0-9_]{0,39}$`).
	Variables VariableSchema `json:"variables"`
	Version   int            `json:"version"`
}

// TemplateVersionCreated defines model for TemplateVersionCreated.
type TemplateVersionCreated struct {
	CreatedAt  time.Time `json:"created_at"`
	TemplateId string    `json:"template_id"`
	Version    int       `json:"version"`
}

// UnsubscribeRequest defines model for UnsubscribeRequest.
type UnsubscribeRequest struct {
	// Token The token from the unsubscribe URL (43 characters, base64url).
	Token string `json:"token"`
}

// UnsubscribeResult defines model for UnsubscribeResult.
type UnsubscribeResult struct {
	ContactId string                  `json:"contact_id"`
	Status    UnsubscribeResultStatus `json:"status"`
}

// UnsubscribeResultStatus defines model for UnsubscribeResult.Status.
type UnsubscribeResultStatus string

// UpdateAutomationRequest defines model for UpdateAutomationRequest.
type UpdateAutomationRequest struct {
	Name *string `json:"name,omitempty"`

	// Status `active`: starts runs. `paused`: starts no new runs; runs in flight continue. `archived`: final; all runs cancelled.
	Status *AutomationStatus `json:"status,omitempty"`
}

// UpdateContactRequest defines model for UpdateContactRequest.
type UpdateContactRequest struct {
	// Attributes Values for personalising broadcasts: at most 20 entries, names
	// `^[a-z][a-z0-9_]{0,39}$` (not `unsubscribe_url`), values strings (at
	// most 1000 characters), finite numbers or booleans; at most 8 KiB as
	// compact JSON. Attributes are matched by name to a template's
	// declared variables; undeclared ones are ignored. Never logged.
	Attributes *ContactAttributes `json:"attributes,omitempty"`

	// ConsentSource Required whenever `status` is `subscribed` in the same request.
	ConsentSource *string `json:"consent_source,omitempty"`

	// Status `subscribed`: the person consented to broadcasts. `unsubscribed`: no broadcast is sent to the contact.
	Status *ContactStatus `json:"status,omitempty"`
}

// Usage defines model for Usage.
type Usage struct {
	Data   []UsageDay `json:"data"`
	From   string     `json:"from"`
	Limits struct {
		ContactEventsPerMinute int `json:"contact_events_per_minute"`
		SendsPerMinute         int `json:"sends_per_minute"`
	} `json:"limits"`
	To     string      `json:"to"`
	Totals UsageCounts `json:"totals"`
}

// UsageCounts defines model for UsageCounts.
type UsageCounts struct {
	Accepted   int64 `json:"accepted"`
	Bounced    int64 `json:"bounced"`
	Canceled   int64 `json:"canceled"`
	Complained int64 `json:"complained"`
	Delivered  int64 `json:"delivered"`
	Failed     int64 `json:"failed"`
	Sent       int64 `json:"sent"`
}

// UsageDay defines model for UsageDay.
type UsageDay struct {
	Accepted   int64  `json:"accepted"`
	Bounced    int64  `json:"bounced"`
	Canceled   int64  `json:"canceled"`
	Complained int64  `json:"complained"`
	Date       string `json:"date"`
	Delivered  int64  `json:"delivered"`
	Failed     int64  `json:"failed"`
	Sent       int64  `json:"sent"`
}

// VariableSchema Declared variables by name (`^[a-z][a-z0-9_]{0,39}$`).
type VariableSchema map[string]VariableSpec

// VariableSpec defines model for VariableSpec.
type VariableSpec struct {
	// MaxLength Strings only; default 1000 characters.
	MaxLength *int             `json:"max_length,omitempty"`
	Required  *bool            `json:"required,omitempty"`
	Type      VariableSpecType `json:"type"`
}

// VariableSpecType defines model for VariableSpec.Type.
type VariableSpecType string

// WaitStep defines model for WaitStep.
type WaitStep struct {
	Seconds int         `json:"seconds"`
	Type    interface{} `json:"type"`
}

// WebhookEndpointCreated defines model for WebhookEndpointCreated.
type WebhookEndpointCreated struct {
	CreatedAt  time.Time          `json:"created_at"`
	EventTypes []WebhookEventType `json:"event_types"`
	Id         string             `json:"id"`

	// Secret Signing secret (`whsec_…`). Shown only in this response.
	Secret string                       `json:"secret"`
	Status WebhookEndpointCreatedStatus `json:"status"`
	Url    string                       `json:"url"`
}

// WebhookEndpointCreatedStatus defines model for WebhookEndpointCreated.Status.
type WebhookEndpointCreatedStatus string

// WebhookEvent defines model for WebhookEvent.
type WebhookEvent struct {
	// CreatedAt When the platform received and stored the event.
	CreatedAt time.Time `json:"created_at"`
	Data      struct {
		EmailId   string `json:"email_id"`
		ProjectId string `json:"project_id"`
	} `json:"data"`

	// Id Event id, stable across retries; equals `Webhook-Id`.
	Id   string           `json:"id"`
	Type WebhookEventType `json:"type"`
}

// WebhookEventType defines model for WebhookEventType.
type WebhookEventType string

// ContactID defines model for ContactID.
type ContactID = string

// Cursor defines model for Cursor.
type Cursor = string

// Limit defines model for Limit.
type Limit = int

// ResourceID defines model for ResourceID.
type ResourceID = string

// TemplateID defines model for TemplateID.
type TemplateID = string

// BadRequest defines model for BadRequest.
type BadRequest = Error

// Conflict defines model for Conflict.
type Conflict = Error

// Forbidden defines model for Forbidden.
type Forbidden = Error

// InternalError defines model for InternalError.
type InternalError = Error

// NotFound defines model for NotFound.
type NotFound = Error

// PayloadTooLarge defines model for PayloadTooLarge.
type PayloadTooLarge = Error

// ServiceUnavailable defines model for ServiceUnavailable.
type ServiceUnavailable = Error

// TooManyRequests defines model for TooManyRequests.
type TooManyRequests = Error

// Unauthorized defines model for Unauthorized.
type Unauthorized = Error

// UnprocessableEntity defines model for UnprocessableEntity.
type UnprocessableEntity = Error

// UnsupportedMediaType defines model for UnsupportedMediaType.
type UnsupportedMediaType = Error

// ListAudiencesParams defines parameters for ListAudiences.
type ListAudiencesParams struct {
	// Limit Page size; values outside 1–100 give `400 invalid_limit`.
	Limit *Limit `form:"limit,omitempty" json:"limit,omitempty"`

	// Cursor Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`.
	Cursor *Cursor `form:"cursor,omitempty" json:"cursor,omitempty"`
}

// ListAudienceContactsParams defines parameters for ListAudienceContacts.
type ListAudienceContactsParams struct {
	// Limit Page size; values outside 1–100 give `400 invalid_limit`.
	Limit *Limit `form:"limit,omitempty" json:"limit,omitempty"`

	// Cursor Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`.
	Cursor *Cursor `form:"cursor,omitempty" json:"cursor,omitempty"`
}

// ListAutomationsParams defines parameters for ListAutomations.
type ListAutomationsParams struct {
	Status *AutomationStatus `form:"status,omitempty" json:"status,omitempty"`

	// Limit Page size; values outside 1–100 give `400 invalid_limit`.
	Limit *Limit `form:"limit,omitempty" json:"limit,omitempty"`

	// Cursor Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`.
	Cursor *Cursor `form:"cursor,omitempty" json:"cursor,omitempty"`
}

// ListAutomationRunsParams defines parameters for ListAutomationRuns.
type ListAutomationRunsParams struct {
	Status    *AutomationRunStatus `form:"status,omitempty" json:"status,omitempty"`
	ContactId *string              `form:"contact_id,omitempty" json:"contact_id,omitempty"`

	// Limit Page size; values outside 1–100 give `400 invalid_limit`.
	Limit *Limit `form:"limit,omitempty" json:"limit,omitempty"`

	// Cursor Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`.
	Cursor *Cursor `form:"cursor,omitempty" json:"cursor,omitempty"`
}

// ListBroadcastsParams defines parameters for ListBroadcasts.
type ListBroadcastsParams struct {
	Status *BroadcastStatus `form:"status,omitempty" json:"status,omitempty"`

	// Limit Page size; values outside 1–100 give `400 invalid_limit`.
	Limit *Limit `form:"limit,omitempty" json:"limit,omitempty"`

	// Cursor Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`.
	Cursor *Cursor `form:"cursor,omitempty" json:"cursor,omitempty"`
}

// CreateContactEventParams defines parameters for CreateContactEvent.
type CreateContactEventParams struct {
	IdempotencyKey *string `json:"Idempotency-Key,omitempty"`
}

// ListContactsParams defines parameters for ListContacts.
type ListContactsParams struct {
	// Email Exact address, case-insensitive.
	Email  *string        `form:"email,omitempty" json:"email,omitempty"`
	Status *ContactStatus `form:"status,omitempty" json:"status,omitempty"`

	// Limit Page size; values outside 1–100 give `400 invalid_limit`.
	Limit *Limit `form:"limit,omitempty" json:"limit,omitempty"`

	// Cursor Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`.
	Cursor *Cursor `form:"cursor,omitempty" json:"cursor,omitempty"`
}

// ListEmailsParams defines parameters for ListEmails.
type ListEmailsParams struct {
	// Status Repeat for several statuses (at most 9).
	Status *[]EmailStatus `form:"status,omitempty" json:"status,omitempty"`

	// Recipient Exact recipient address, case-insensitive.
	Recipient *string `form:"recipient,omitempty" json:"recipient,omitempty"`

	// TemplateId Only emails sent from this template (available together with template sends).
	TemplateId *string `form:"template_id,omitempty" json:"template_id,omitempty"`

	// BroadcastId Only recipient emails of this broadcast.
	BroadcastId *string `form:"broadcast_id,omitempty" json:"broadcast_id,omitempty"`

	// CreatedAfter Inclusive lower bound (RFC 3339).
	CreatedAfter *time.Time `form:"created_after,omitempty" json:"created_after,omitempty"`

	// CreatedBefore Exclusive upper bound (RFC 3339); must be later than `created_after`.
	CreatedBefore *time.Time `form:"created_before,omitempty" json:"created_before,omitempty"`

	// Limit Page size; values outside 1–100 give `400 invalid_limit`.
	Limit *int `form:"limit,omitempty" json:"limit,omitempty"`

	// Cursor Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`.
	Cursor *string `form:"cursor,omitempty" json:"cursor,omitempty"`
}

// CreateEmailParams defines parameters for CreateEmail.
type CreateEmailParams struct {
	// IdempotencyKey Client-chosen key, 1–255 printable ASCII characters without spaces
	// (0x21–0x7E), sent at most once. Empty, repeated or invalid keys
	// are rejected with `400 invalid_idempotency_key`. Expires 24 hours
	// after first use: a retry later than that creates a new email.
	IdempotencyKey *string `json:"Idempotency-Key,omitempty"`
}

// ListEmailEventsParams defines parameters for ListEmailEvents.
type ListEmailEventsParams struct {
	// Limit Page size; values outside 1–100 give `400 invalid_limit`.
	Limit *int `form:"limit,omitempty" json:"limit,omitempty"`

	// Cursor Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`.
	Cursor *string `form:"cursor,omitempty" json:"cursor,omitempty"`
}

// GetUsageParams defines parameters for GetUsage.
type GetUsageParams struct {
	// From First UTC day (`YYYY-MM-DD`); default `to` minus 29 days.
	From *string `form:"from,omitempty" json:"from,omitempty"`

	// To Last UTC day (`YYYY-MM-DD`); default today (UTC).
	To *string `form:"to,omitempty" json:"to,omitempty"`
}

// EmailStatusChangedParams defines parameters for EmailStatusChanged.
type EmailStatusChangedParams struct {
	WebhookId string `json:"Webhook-Id"`

	// WebhookTimestamp Unix time in seconds when this attempt was signed.
	WebhookTimestamp string `json:"Webhook-Timestamp"`
	WebhookSignature string `json:"Webhook-Signature"`
}

// CreateAudienceJSONRequestBody defines body for CreateAudience for application/json ContentType.
type CreateAudienceJSONRequestBody = CreateAudienceRequest

// AddAudienceContactsJSONRequestBody defines body for AddAudienceContacts for application/json ContentType.
type AddAudienceContactsJSONRequestBody = AddAudienceContactsRequest

// CreateAutomationJSONRequestBody defines body for CreateAutomation for application/json ContentType.
type CreateAutomationJSONRequestBody = CreateAutomationRequest

// UpdateAutomationJSONRequestBody defines body for UpdateAutomation for application/json ContentType.
type UpdateAutomationJSONRequestBody = UpdateAutomationRequest

// CreateBroadcastJSONRequestBody defines body for CreateBroadcast for application/json ContentType.
type CreateBroadcastJSONRequestBody = CreateBroadcastRequest

// ScheduleBroadcastJSONRequestBody defines body for ScheduleBroadcast for application/json ContentType.
type ScheduleBroadcastJSONRequestBody = ScheduleBroadcastRequest

// CreateContactEventJSONRequestBody defines body for CreateContactEvent for application/json ContentType.
type CreateContactEventJSONRequestBody = CreateContactEventRequest

// CreateContactJSONRequestBody defines body for CreateContact for application/json ContentType.
type CreateContactJSONRequestBody = CreateContactRequest

// UpdateContactJSONRequestBody defines body for UpdateContact for application/json ContentType.
type UpdateContactJSONRequestBody = UpdateContactRequest

// CreateEmailJSONRequestBody defines body for CreateEmail for application/json ContentType.
type CreateEmailJSONRequestBody = CreateEmailRequest

// CreateTemplateJSONRequestBody defines body for CreateTemplate for application/json ContentType.
type CreateTemplateJSONRequestBody = CreateTemplateRequest

// PublishTemplateVersionJSONRequestBody defines body for PublishTemplateVersion for application/json ContentType.
type PublishTemplateVersionJSONRequestBody = PublishTemplateVersionRequest

// ReportUnsubscribeJSONRequestBody defines body for ReportUnsubscribe for application/json ContentType.
type ReportUnsubscribeJSONRequestBody = UnsubscribeRequest

// CreateWebhookEndpointJSONRequestBody defines body for CreateWebhookEndpoint for application/json ContentType.
type CreateWebhookEndpointJSONRequestBody = CreateWebhookEndpointRequest

// EmailStatusChangedJSONRequestBody defines body for EmailStatusChanged for application/json ContentType.
type EmailStatusChangedJSONRequestBody = WebhookEvent
