package testaws

import (
	"crypto/md5" //nolint:gosec // SQS defines MD5OfBody; an integrity checksum, not a security control
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// SQS stands in for one SQS queue over the JSON protocol: ReceiveMessage
// (long poll, visibility timeout) and DeleteMessage. Every request is
// recorded; Fail injects error answers.
type SQS struct {
	URL string

	mu       sync.Mutex
	messages []*sqsMessage
	deleted  []string // bodies of deleted messages, in delete order
	requests []SQSRequest
	fail     func(SQSRequest) Response
	nextID   int
	arrived  chan struct{} // closed and replaced when a message is added
	corrupt  bool
}

type sqsMessage struct {
	id, body, receipt string
	receives          int
	visibleAt         time.Time
}

// SQSRequest is one recorded request.
type SQSRequest struct {
	Operation           string // ReceiveMessage, DeleteMessage, …
	QueueURL            string
	WaitTimeSeconds     int
	MaxNumberOfMessages int
	VisibilityTimeout   int
	SystemAttributes    []string
	ReceiptHandle       string
	Authorization       string // the request's SigV4 Authorization header
}

// NewSQS starts a stub closed at the end of the test.
func NewSQS(t testing.TB) *SQS {
	t.Helper()
	s := &SQS{arrived: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// Add puts a message with body on the queue, visible now.
func (s *SQS) Add(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.messages = append(s.messages, &sqsMessage{id: "stub-sqs-" + strconv.Itoa(s.nextID), body: body})
	close(s.arrived)
	s.arrived = make(chan struct{})
}

// Fail sets an answer for the following requests; a zero Response (or a nil
// fn) lets a request through.
func (s *SQS) Fail(fn func(SQSRequest) Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = fn
}

// CorruptChecksums makes every following answer carry a wrong MD5OfBody.
func (s *SQS) CorruptChecksums() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.corrupt = true
}

// ExpireVisibility makes every received, undeleted message visible again,
// as if its visibility timeout had passed.
func (s *SQS) ExpireVisibility() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		m.visibleAt = time.Time{}
	}
	close(s.arrived)
	s.arrived = make(chan struct{})
}

// Pending returns the number of messages not deleted yet.
func (s *SQS) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

// Deleted returns the bodies of deleted messages.
func (s *SQS) Deleted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...)
}

// Requests returns the requests received so far.
func (s *SQS) Requests() []SQSRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SQSRequest(nil), s.requests...)
}

type sqsBody struct {
	QueueURL                    string   `json:"QueueUrl"`
	WaitTimeSeconds             int      `json:"WaitTimeSeconds"`
	MaxNumberOfMessages         int      `json:"MaxNumberOfMessages"`
	VisibilityTimeout           int      `json:"VisibilityTimeout"`
	MessageSystemAttributeNames []string `json:"MessageSystemAttributeNames"`
	ReceiptHandle               string   `json:"ReceiptHandle"`
}

type sqsOut struct {
	MessageID     string `json:"MessageId"`
	ReceiptHandle string
	MD5OfBody     string
	Body          string
	Attributes    map[string]string `json:",omitempty"`
}

const targetPrefix = "AmazonSQS."

func (s *SQS) serve(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	if r.Method != http.MethodPost || r.URL.Path != "/" || len(target) <= len(targetPrefix) || target[:len(targetPrefix)] != targetPrefix {
		http.Error(w, "unexpected request", http.StatusNotFound)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var body sqsBody
	if err := json.Unmarshal(data, &body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req := SQSRequest{
		Operation: target[len(targetPrefix):], QueueURL: body.QueueURL, WaitTimeSeconds: body.WaitTimeSeconds,
		MaxNumberOfMessages: body.MaxNumberOfMessages, VisibilityTimeout: body.VisibilityTimeout,
		SystemAttributes: body.MessageSystemAttributeNames, ReceiptHandle: body.ReceiptHandle,
		Authorization: r.Header.Get("Authorization"),
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	fail := s.fail
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	if fail != nil {
		if resp := fail(req); resp.Status != 0 {
			if resp.Delay > 0 {
				select {
				case <-time.After(resp.Delay):
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(resp.Status)
			writeJSON(w, map[string]string{"__type": resp.ErrorType, "message": resp.Message})
			return
		}
	}
	switch req.Operation {
	case "ReceiveMessage":
		s.receive(w, r, req)
	case "DeleteMessage":
		s.delete(w, req)
	default:
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"__type": "UnsupportedOperation", "message": "stub"})
	}
}

// receive answers with up to MaxNumberOfMessages visible messages, waiting
// up to WaitTimeSeconds for one to arrive.
func (s *SQS) receive(w http.ResponseWriter, r *http.Request, req SQSRequest) {
	deadline := time.After(time.Duration(req.WaitTimeSeconds) * time.Second)
	for {
		s.mu.Lock()
		out := s.take(req)
		arrived := s.arrived
		s.mu.Unlock()
		if len(out) > 0 || req.WaitTimeSeconds == 0 {
			writeJSON(w, map[string][]sqsOut{"Messages": out})
			return
		}
		select {
		case <-arrived:
		case <-deadline:
			writeJSON(w, map[string][]sqsOut{})
			return
		case <-r.Context().Done():
			return
		}
	}
}

// take hides and returns visible messages; s.mu is held.
func (s *SQS) take(req SQSRequest) []sqsOut {
	limit := max(req.MaxNumberOfMessages, 1)
	now := time.Now()
	var out []sqsOut
	for _, m := range s.messages {
		if len(out) == limit {
			break
		}
		if now.Before(m.visibleAt) {
			continue
		}
		m.receives++
		m.receipt = m.id + "#" + strconv.Itoa(m.receives)
		m.visibleAt = now.Add(time.Duration(req.VisibilityTimeout) * time.Second)
		sum := md5.Sum([]byte(m.body)) //nolint:gosec // see import
		if s.corrupt {
			sum[0] ^= 0xff
		}
		o := sqsOut{MessageID: m.id, ReceiptHandle: m.receipt, MD5OfBody: hex.EncodeToString(sum[:]), Body: m.body}
		for _, a := range req.SystemAttributes {
			if a == "ApproximateReceiveCount" || a == "All" {
				o.Attributes = map[string]string{"ApproximateReceiveCount": strconv.Itoa(m.receives)}
			}
		}
		out = append(out, o)
	}
	return out
}

// delete removes the message whose latest receipt handle is given. Like SQS
// it answers success for an unknown handle.
func (s *SQS) delete(w http.ResponseWriter, req SQSRequest) {
	s.mu.Lock()
	for i, m := range s.messages {
		if m.receipt != "" && m.receipt == req.ReceiptHandle {
			s.deleted = append(s.deleted, m.body)
			s.messages = append(s.messages[:i], s.messages[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	_, _ = io.WriteString(w, "{}")
}
