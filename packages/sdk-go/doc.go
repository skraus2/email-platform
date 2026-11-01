// Package emailplatform is the Go client for the email platform's /v1 API.
//
//	ep, err := emailplatform.New(os.Getenv("EP_API_KEY"), "https://api.example.com")
//	email, err := ep.Emails.Send(ctx, &emailplatform.CreateEmailRequest{…},
//		emailplatform.WithIdempotencyKey("order-42:receipt"))
//
// The request and response types in this package are generated from the
// API contract and keep its wire names. Errors are *APIError (a response
// outside 2xx) or *TransportError (no usable response); argument errors
// wrap ErrInvalidArgument. Webhook requests are verified with the webhook
// subpackage.
package emailplatform
