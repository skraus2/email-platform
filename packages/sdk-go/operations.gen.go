// Code generated from api/openapi.yaml by tools/oapi-codegen; DO NOT EDIT.
// Regenerate with `make sdk-go-generate`.

package emailplatform

// operation is one API operation: its operationId, HTTP method and path template.
type operation struct{ id, method, path string }

// operations lists every operation of the API contract.
var operations = [...]operation{
	{"listAudiences", "GET", "/v1/audiences"},
	{"createAudience", "POST", "/v1/audiences"},
	{"getAudience", "GET", "/v1/audiences/{id}"},
	{"listAudienceContacts", "GET", "/v1/audiences/{id}/contacts"},
	{"addAudienceContacts", "POST", "/v1/audiences/{id}/contacts"},
	{"removeAudienceContact", "DELETE", "/v1/audiences/{id}/contacts/{contact_id}"},
	{"addAudienceContact", "PUT", "/v1/audiences/{id}/contacts/{contact_id}"},
	{"getAutomationRun", "GET", "/v1/automation-runs/{id}"},
	{"cancelAutomationRun", "POST", "/v1/automation-runs/{id}/cancel"},
	{"listAutomations", "GET", "/v1/automations"},
	{"createAutomation", "POST", "/v1/automations"},
	{"getAutomation", "GET", "/v1/automations/{id}"},
	{"updateAutomation", "PATCH", "/v1/automations/{id}"},
	{"listAutomationRuns", "GET", "/v1/automations/{id}/runs"},
	{"listBroadcasts", "GET", "/v1/broadcasts"},
	{"createBroadcast", "POST", "/v1/broadcasts"},
	{"getBroadcast", "GET", "/v1/broadcasts/{id}"},
	{"cancelBroadcast", "POST", "/v1/broadcasts/{id}/cancel"},
	{"scheduleBroadcast", "POST", "/v1/broadcasts/{id}/schedule"},
	{"createContactEvent", "POST", "/v1/contact-events"},
	{"listContacts", "GET", "/v1/contacts"},
	{"createContact", "POST", "/v1/contacts"},
	{"deleteContact", "DELETE", "/v1/contacts/{id}"},
	{"getContact", "GET", "/v1/contacts/{id}"},
	{"updateContact", "PATCH", "/v1/contacts/{id}"},
	{"listEmails", "GET", "/v1/emails"},
	{"createEmail", "POST", "/v1/emails"},
	{"getEmail", "GET", "/v1/emails/{id}"},
	{"cancelEmail", "POST", "/v1/emails/{id}/cancel"},
	{"listEmailEvents", "GET", "/v1/emails/{id}/events"},
	{"createTemplate", "POST", "/v1/templates"},
	{"getTemplate", "GET", "/v1/templates/{id}"},
	{"publishTemplateVersion", "POST", "/v1/templates/{id}/versions"},
	{"getTemplateVersion", "GET", "/v1/templates/{id}/versions/{version}"},
	{"reportUnsubscribe", "POST", "/v1/unsubscribes"},
	{"getUsage", "GET", "/v1/usage"},
	{"createWebhookEndpoint", "POST", "/v1/webhook-endpoints"},
}
