// Package testaws provides local stand-ins for the AWS APIs the platform
// calls. They listen on loopback only, so tests exercise the real SDK
// clients without ever contacting AWS.
package testaws

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// SESv2 stands in for the SESv2 SendEmail operation. It records every request
// and answers with the response chosen by Respond (default: success).
type SESv2 struct {
	URL string

	mu      sync.Mutex
	sends   []SendEmail
	respond func(SendEmail) Response
	next    int
}

// SendEmail is one recorded SendEmail request.
type SendEmail struct {
	ConfigurationSet string
	From             string
	To               []string
	Raw              []byte
	Tags             map[string]string
	TagCount         int
	Authorization    string // the request's SigV4 Authorization header
}

// Response is what the stub answers. A zero Status means 200 with MessageID
// (or a generated id).
type Response struct {
	Status    int
	ErrorType string // X-Amzn-ErrorType of an error response
	Message   string // error message text
	MessageID string
	Body      string        // raw body instead of the generated one
	Delay     time.Duration // wait before answering (until the client gives up)
}

// NewSESv2 starts a stub closed at the end of the test.
func NewSESv2(t testing.TB) *SESv2 {
	t.Helper()
	s := &SESv2{}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// Respond sets how the stub answers the following requests.
func (s *SESv2) Respond(fn func(SendEmail) Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.respond = fn
}

// Sends returns the requests received so far.
func (s *SESv2) Sends() []SendEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SendEmail(nil), s.sends...)
}

type sendEmailBody struct {
	ConfigurationSetName string `json:"ConfigurationSetName"`
	FromEmailAddress     string `json:"FromEmailAddress"`
	Destination          struct {
		ToAddresses []string `json:"ToAddresses"`
	} `json:"Destination"`
	Content struct {
		Raw struct {
			Data []byte `json:"Data"` // base64 in JSON
		} `json:"Raw"`
	} `json:"Content"`
	EmailTags []struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	} `json:"EmailTags"`
}

func (s *SESv2) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v2/email/outbound-emails" {
		http.Error(w, "unexpected request", http.StatusNotFound)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 50<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var body sendEmailBody
	if err := json.Unmarshal(data, &body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req := SendEmail{
		ConfigurationSet: body.ConfigurationSetName,
		From:             body.FromEmailAddress,
		To:               body.Destination.ToAddresses,
		Raw:              body.Content.Raw.Data,
		Tags:             map[string]string{},
		TagCount:         len(body.EmailTags),
		Authorization:    r.Header.Get("Authorization"),
	}
	for _, tag := range body.EmailTags {
		req.Tags[tag.Name] = tag.Value
	}

	s.mu.Lock()
	s.sends = append(s.sends, req)
	s.next++
	n := s.next
	respond := s.respond
	s.mu.Unlock()

	resp := Response{}
	if respond != nil {
		resp = respond(req)
	}
	if resp.Delay > 0 {
		select {
		case <-time.After(resp.Delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if resp.Status != 0 && resp.Status != http.StatusOK {
		if resp.ErrorType != "" {
			w.Header().Set("X-Amzn-ErrorType", resp.ErrorType)
		}
		w.WriteHeader(resp.Status)
		if resp.Body != "" {
			_, _ = io.WriteString(w, resp.Body)
			return
		}
		writeJSON(w, map[string]string{"message": resp.Message})
		return
	}
	if resp.Body != "" {
		_, _ = io.WriteString(w, resp.Body)
		return
	}
	id := resp.MessageID
	if id == "" {
		id = "stub-message-" + strconv.Itoa(n)
	}
	writeJSON(w, map[string]string{"MessageId": id})
}

// writeJSON answers with v. A failed write means the client is gone, which a
// stub has no use for.
func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // stub answers are maps and slices of strings; write errors are ignored
}
