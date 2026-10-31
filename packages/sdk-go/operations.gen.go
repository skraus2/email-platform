// Code generated from api/openapi.yaml by tools/oapi-codegen; DO NOT EDIT.
// Regenerate with `make sdk-go-generate`.

package emailplatform

// operation is one API operation: its operationId, HTTP method and path
// template, and the Go types of its JSON request body (bodyMode "required",
// "optional" or "" for none), query parameters and success result ("" when
// the success response has no body).
type operation struct{ id, method, path, body, bodyMode, params, result string }

// operations lists every operation of the API contract.
var operations = [...]operation{
	{"listAudiences", "GET", "/v1/audiences", "", "", "ListAudiencesParams", "AudienceList"},
	{"createAudience", "POST", "/v1/audiences", "CreateAudienceRequest", "required", "", "Audience"},
	{"getAudience", "GET", "/v1/audiences/{id}", "", "", "", "AudienceWithCount"},
	{"listAudienceContacts", "GET", "/v1/audiences/{id}/contacts", "", "", "ListAudienceContactsParams", "ContactList"},
	{"addAudienceContacts", "POST", "/v1/audiences/{id}/contacts", "AddAudienceContactsRequest", "required", "", "AddAudienceContactsResult"},
	{"removeAudienceContact", "DELETE", "/v1/audiences/{id}/contacts/{contact_id}", "", "", "", ""},
	{"addAudienceContact", "PUT", "/v1/audiences/{id}/contacts/{contact_id}", "", "", "", ""},
	{"getAutomationRun", "GET", "/v1/automation-runs/{id}", "", "", "", "AutomationRunWithEmails"},
	{"cancelAutomationRun", "POST", "/v1/automation-runs/{id}/cancel", "", "", "", "AutomationRun"},
	{"listAutomations", "GET", "/v1/automations", "", "", "ListAutomationsParams", "AutomationList"},
	{"createAutomation", "POST", "/v1/automations", "CreateAutomationRequest", "required", "", "Automation"},
	{"getAutomation", "GET", "/v1/automations/{id}", "", "", "", "AutomationWithCounts"},
	{"updateAutomation", "PATCH", "/v1/automations/{id}", "UpdateAutomationRequest", "required", "", "Automation"},
	{"listAutomationRuns", "GET", "/v1/automations/{id}/runs", "", "", "ListAutomationRunsParams", "AutomationRunList"},
	{"listBroadcasts", "GET", "/v1/broadcasts", "", "", "ListBroadcastsParams", "BroadcastList"},
	{"createBroadcast", "POST", "/v1/broadcasts", "CreateBroadcastRequest", "required", "", "Broadcast"},
	{"getBroadcast", "GET", "/v1/broadcasts/{id}", "", "", "", "BroadcastWithCounts"},
	{"cancelBroadcast", "POST", "/v1/broadcasts/{id}/cancel", "", "", "", "Broadcast"},
	{"scheduleBroadcast", "POST", "/v1/broadcasts/{id}/schedule", "ScheduleBroadcastRequest", "optional", "", "Broadcast"},
	{"createContactEvent", "POST", "/v1/contact-events", "CreateContactEventRequest", "required", "", "ContactEvent"},
	{"listContacts", "GET", "/v1/contacts", "", "", "ListContactsParams", "ContactList"},
	{"createContact", "POST", "/v1/contacts", "CreateContactRequest", "required", "", "Contact"},
	{"deleteContact", "DELETE", "/v1/contacts/{id}", "", "", "", ""},
	{"getContact", "GET", "/v1/contacts/{id}", "", "", "", "Contact"},
	{"updateContact", "PATCH", "/v1/contacts/{id}", "UpdateContactRequest", "required", "", "Contact"},
	{"listEmails", "GET", "/v1/emails", "", "", "ListEmailsParams", "EmailList"},
	{"createEmail", "POST", "/v1/emails", "CreateEmailRequest", "required", "", "CreateEmailResponse"},
	{"getEmail", "GET", "/v1/emails/{id}", "", "", "", "Email"},
	{"cancelEmail", "POST", "/v1/emails/{id}/cancel", "", "", "", "Email"},
	{"listEmailEvents", "GET", "/v1/emails/{id}/events", "", "", "ListEmailEventsParams", "EmailEventList"},
	{"createTemplate", "POST", "/v1/templates", "CreateTemplateRequest", "required", "", "Template"},
	{"getTemplate", "GET", "/v1/templates/{id}", "", "", "", "Template"},
	{"publishTemplateVersion", "POST", "/v1/templates/{id}/versions", "PublishTemplateVersionRequest", "required", "", "TemplateVersionCreated"},
	{"getTemplateVersion", "GET", "/v1/templates/{id}/versions/{version}", "", "", "", "TemplateVersion"},
	{"reportUnsubscribe", "POST", "/v1/unsubscribes", "UnsubscribeRequest", "required", "", "UnsubscribeResult"},
	{"getUsage", "GET", "/v1/usage", "", "", "GetUsageParams", "Usage"},
	{"createWebhookEndpoint", "POST", "/v1/webhook-endpoints", "CreateWebhookEndpointRequest", "required", "", "WebhookEndpointCreated"},
}
