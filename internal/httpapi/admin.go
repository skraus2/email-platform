package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/admin"
	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/templates"
)

// AdminSessions is what the admin API needs from the account service.
type AdminSessions interface {
	Login(ctx context.Context, username, password string) (admin.Session, error)
	Authenticate(ctx context.Context, token string) (admin.Principal, error)
	Logout(ctx context.Context, token string) error
}

// AdminDeps are the collaborators of the admin API.
type AdminDeps struct {
	Logger   *slog.Logger
	Sessions AdminSessions
	Views    *admin.Views
	// ListenHost is the configured listener host; requests must name it,
	// a loopback address or localhost in their Host header.
	ListenHost string
}

// AdminServer is the operator API described in api/admin-openapi.yaml. It
// is served on its own listener; project API keys are never accepted and
// session tokens only from the Authorization header, never from cookies.
type AdminServer struct {
	logger   *slog.Logger
	sessions AdminSessions
	views    *admin.Views
	handler  http.Handler
}

type adminPrincipalKey struct{}

// NewAdmin builds the admin API handler.
func NewAdmin(d AdminDeps) *AdminServer {
	s := &AdminServer{logger: d.Logger, sessions: d.Sessions, views: d.Views}
	mux := http.NewServeMux()
	// Each route also answers other methods on its path with 405 (the
	// catch-all would make them 404s).
	route := func(pattern, allow string, h http.HandlerFunc) {
		_, path, _ := strings.Cut(pattern, " ")
		mux.HandleFunc(pattern, h)
		mux.HandleFunc(path, methodNotAllowed(allow))
	}
	route("POST /admin/v1/sessions", http.MethodPost, s.handleLogin)
	route("DELETE /admin/v1/sessions/current", http.MethodDelete, s.requireSession(s.handleLogout))
	route("GET /admin/v1/me", http.MethodGet, s.requireSession(s.handleMe))
	route("GET /admin/v1/projects", http.MethodGet, s.requireSession(s.handleProjects))
	route("GET /admin/v1/emails", http.MethodGet, s.requireSession(s.handleEmails))
	route("GET /admin/v1/emails/{id}", http.MethodGet, s.requireSession(s.handleEmail))
	route("GET /admin/v1/emails/{id}/events", http.MethodGet, s.requireSession(s.handleEmailEvents))
	route("GET /admin/v1/emails/{id}/webhook-deliveries", http.MethodGet, s.requireSession(s.handleEmailDeliveries))
	route("GET /admin/v1/templates", http.MethodGet, s.requireSession(s.handleTemplates))
	route("GET /admin/v1/templates/{id}", http.MethodGet, s.requireSession(s.handleTemplate))
	route("GET /admin/v1/templates/{id}/versions/{version}", http.MethodGet, s.requireSession(s.handleTemplateVersion))
	route("POST /admin/v1/templates/{id}/versions/{version}/preview", http.MethodPost, s.requireSession(s.handlePreview))
	route("GET /admin/v1/senders", http.MethodGet, s.requireSession(s.handleSenders))
	route("GET /admin/v1/broadcasts", http.MethodGet, s.requireSession(s.handleBroadcasts))
	route("GET /admin/v1/broadcasts/{id}", http.MethodGet, s.requireSession(s.handleBroadcast))
	route("GET /admin/v1/automations", http.MethodGet, s.requireSession(s.handleAutomations))
	route("GET /admin/v1/automations/{id}", http.MethodGet, s.requireSession(s.handleAutomation))
	route("GET /admin/v1/automations/{id}/runs", http.MethodGet, s.requireSession(s.handleAutomationRuns))
	route("GET /admin/v1/automation-runs/{id}", http.MethodGet, s.requireSession(s.handleAutomationRun))
	route("GET /admin/v1/webhook-endpoints", http.MethodGet, s.requireSession(s.handleWebhookEndpoints))
	mux.HandleFunc("/", handleNotFound)
	s.handler = requestLogger(s.logger, allowHosts(d.ListenHost, mux))
	return s
}

// allowHosts refuses requests whose Host is neither the listener's own host,
// a loopback address nor localhost: a web page that rebinds its DNS name to
// the admin listener would otherwise talk to it as same-origin.
func allowHosts(listenHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
		ip, err := netip.ParseAddr(host)
		ok := strings.EqualFold(host, "localhost") || (err == nil && ip.IsLoopback()) ||
			(listenHost != "" && strings.EqualFold(host, strings.Trim(listenHost, "[]")))
		if !ok {
			writeError(w, http.StatusMisdirectedRequest, "misdirected_request", "this host name is not served here")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler returns the root handler including middleware.
func (s *AdminServer) Handler() http.Handler { return s.handler }

func adminUnauthenticated(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, "unauthenticated", "a valid admin session is required")
}

// sessionToken extracts the token of "Authorization: Session <token>".
// Bearer (project API keys) and cookies are never accepted.
func sessionToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Session") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

func (s *AdminServer) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := sessionToken(r.Header.Get("Authorization"))
		if !ok {
			adminUnauthenticated(w)
			return
		}
		p, err := s.sessions.Authenticate(r.Context(), token)
		switch {
		case errors.Is(err, admin.ErrUnauthenticated):
			adminUnauthenticated(w)
			return
		case err != nil:
			s.serverError(w, r, "admin authentication", err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), adminPrincipalKey{}, p)))
	}
}

func adminPrincipalFrom(ctx context.Context) admin.Principal {
	p, _ := ctx.Value(adminPrincipalKey{}).(admin.Principal)
	return p
}

// serverError answers 503 for an unavailable database or busy view, 500
// otherwise; details are logged, never returned.
func (s *AdminServer) serverError(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, admin.ErrViewBusy):
		writeUnavailable(w)
	case r.Context().Err() != nil:
		s.logger.DebugContext(r.Context(), op+": client cancelled")
		writeUnavailable(w)
	case database.Unavailable(err):
		s.logger.ErrorContext(r.Context(), op+": database unavailable", slog.Any("error", err))
		writeUnavailable(w)
	default:
		s.logger.ErrorContext(r.Context(), op+" failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *AdminServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req loginRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	sess, err := s.sessions.Login(r.Context(), req.Username, req.Password)
	var locked *admin.LockedError
	switch {
	case errors.Is(err, admin.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is wrong")
		return
	case errors.As(err, &locked):
		w.Header().Set("Retry-After", strconv.Itoa(int((locked.RetryAfter+time.Second-1)/time.Second)))
		writeError(w, http.StatusTooManyRequests, "login_locked", "too many failed logins; retry after the time in Retry-After")
		return
	case errors.Is(err, admin.ErrBusy):
		writeUnavailable(w)
		return
	case err != nil:
		s.serverError(w, r, "admin login", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": sess.Token, "expires_at": sess.ExpiresAt.UTC()})
}

func (s *AdminServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	token, _ := sessionToken(r.Header.Get("Authorization"))
	err := s.sessions.Logout(r.Context(), token)
	switch {
	case errors.Is(err, admin.ErrUnauthenticated):
		adminUnauthenticated(w)
	case err != nil:
		s.serverError(w, r, "admin logout", err)
	default:
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *AdminServer) handleMe(w http.ResponseWriter, r *http.Request) {
	p := adminPrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"id": p.UserID.String(), "username": p.Username, "session_expires_at": p.SessionExpiresAt.UTC(),
	})
}

// adminQuery validates the query parameters of an admin list: only the
// named filters plus limit and cursor, each at most once (status may
// repeat). Messages name the parameter, never its value.
type adminQuery struct {
	values url.Values
	limit  int
	cursor uuid.UUID
}

func parseAdminQuery(w http.ResponseWriter, r *http.Request, allowed ...string) (adminQuery, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return adminQuery{}, false
	}
	ok := map[string]bool{"limit": true, "cursor": true}
	for _, a := range allowed {
		ok[a] = true
	}
	for k, v := range values {
		if !ok[k] {
			writeError(w, http.StatusBadRequest, "invalid_filter", "unknown query parameter")
			return adminQuery{}, false
		}
		if k != "status" && len(v) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_filter", k+" may be given only once")
			return adminQuery{}, false
		}
	}
	q := adminQuery{values: values, limit: defaultEmailPage}
	if v := values.Get("limit"); values.Has("limit") {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > admin.MaxPage {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a single integer between 1 and 100")
			return adminQuery{}, false
		}
		q.limit = n
	}
	if values.Has("cursor") {
		c, valid := decodeCursor(values.Get("cursor"))
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor must be the next_cursor of a previous page")
			return adminQuery{}, false
		}
		q.cursor = c
	}
	return q, true
}

// uuidParam reads an optional UUID query parameter.
func (q adminQuery) uuidParam(w http.ResponseWriter, name string) (*uuid.UUID, bool) {
	if !q.values.Has(name) {
		return nil, true
	}
	v := q.values.Get(name)
	id, err := uuid.Parse(v)
	if err != nil || len(v) != 36 {
		writeError(w, http.StatusBadRequest, "invalid_filter", name+" must be a UUID")
		return nil, false
	}
	return &id, true
}

func nextCursor(more bool, last uuid.UUID) *string {
	if !more {
		return nil
	}
	c := encodeCursor(last)
	return &c
}

func listResponse[T any](data []T, next *string) map[string]any {
	if data == nil {
		data = []T{}
	}
	return map[string]any{"data": data, "next_cursor": next}
}

func (s *AdminServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAdminQuery(w, r)
	if !ok {
		return
	}
	rows, more, err := s.views.Projects(r.Context(), q.cursor, q.limit)
	if err != nil {
		s.serverError(w, r, "admin list projects", err)
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		data = append(data, map[string]any{"id": p.ID.String(), "name": p.Name, "status": p.Status, "created_at": p.CreatedAt.UTC()})
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func adminEmailJSON(e admin.Email) map[string]any {
	resp := toEmailResponse(e.Email)
	return map[string]any{
		"id": resp.ID, "project_id": e.ProjectID.String(), "project_name": e.ProjectName, "status": resp.Status,
		"from": resp.From, "to": resp.To, "subject": resp.Subject, "provider": e.Provider, "provider_id": e.ProviderID,
		"attempts": resp.Attempts, "error_code": resp.ErrorCode, "template_id": resp.TemplateID,
		"template_version": resp.TemplateVersion, "created_at": resp.CreatedAt, "updated_at": resp.UpdatedAt,
		"finished_at": resp.FinishedAt, "scheduled_at": resp.ScheduledAt, "broadcast_id": resp.BroadcastID,
		"automation_id": resp.AutomationID, "automation_run_id": resp.AutomationRunID,
	}
}

func (s *AdminServer) handleEmails(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAdminQuery(w, r, "project_id", "status", "error_code", "recipient", "template_id",
		"broadcast_id", "created_after", "created_before", "q")
	if !ok {
		return
	}
	f := admin.EmailFilter{Cursor: q.cursor}
	if f.ProjectID, ok = q.uuidParam(w, "project_id"); !ok {
		return
	}
	if f.BroadcastID, ok = q.uuidParam(w, "broadcast_id"); !ok {
		return
	}
	if f.TemplateID, ok = q.uuidParam(w, "template_id"); !ok {
		return
	}
	if st := q.values["status"]; len(st) > 0 {
		if len(st) > maxStatusFilters {
			writeError(w, http.StatusBadRequest, "invalid_filter", "status may be given at most 9 times")
			return
		}
		for _, v := range st {
			if !emails.IsStatus(v) {
				writeError(w, http.StatusBadRequest, "invalid_filter", "status must be an email status")
				return
			}
		}
		f.Statuses = st
	}
	if q.values.Has("error_code") {
		if f.ErrorCode = q.values.Get("error_code"); !validErrorCode(f.ErrorCode) {
			writeError(w, http.StatusBadRequest, "invalid_filter", "error_code must be a failure code")
			return
		}
	}
	if q.values.Has("recipient") {
		if f.Recipient = q.values.Get("recipient"); emails.ValidateAddress(f.Recipient) != nil {
			writeError(w, http.StatusBadRequest, "invalid_filter", "recipient must be a single plain email address")
			return
		}
	}
	for _, b := range []struct {
		name string
		dst  **time.Time
	}{{"created_after", &f.CreatedAfter}, {"created_before", &f.CreatedBefore}} {
		if !q.values.Has(b.name) {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, q.values.Get(b.name))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_filter", b.name+" must be an RFC 3339 timestamp")
			return
		}
		*b.dst = &t
	}
	if f.CreatedAfter != nil && f.CreatedBefore != nil && !f.CreatedAfter.Before(*f.CreatedBefore) {
		writeError(w, http.StatusBadRequest, "invalid_filter", "created_after must be earlier than created_before")
		return
	}
	if f.Query = q.values.Get("q"); utf8.RuneCountInString(f.Query) > 512 {
		writeError(w, http.StatusBadRequest, "invalid_filter", "q must be at most 512 characters")
		return
	}
	rows, more, err := s.views.SearchEmails(r.Context(), f, q.limit)
	if err != nil {
		s.serverError(w, r, "admin search emails", err)
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		data = append(data, adminEmailJSON(e))
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func validErrorCode(c string) bool {
	if c == "" || len(c) > 40 {
		return false
	}
	for _, r := range c {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}
	return true
}

// pathID parses the {id} path value; malformed ids answer 404.
func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return uuid.Nil, false
	}
	return id, true
}

func (s *AdminServer) notFoundOr(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, admin.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	s.serverError(w, r, op, err)
}

func (s *AdminServer) handleEmail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	e, err := s.views.Email(r.Context(), id)
	if err != nil {
		s.notFoundOr(w, r, "admin get email", err)
		return
	}
	writeJSON(w, http.StatusOK, adminEmailJSON(e))
}

func (s *AdminServer) handleEmailEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q, ok := parseAdminQuery(w, r)
	if !ok {
		return
	}
	rows, more, err := s.views.EmailEvents(r.Context(), id, q.cursor, q.limit)
	if err != nil {
		s.notFoundOr(w, r, "admin email events", err)
		return
	}
	data := make([]eventResponse, 0, len(rows))
	for _, e := range rows {
		data = append(data, eventResponse{
			ID: e.ID.String(), Type: e.Type, Origin: e.Origin, Provider: e.Provider,
			OccurredAt: e.OccurredAt.UTC(), ReceivedAt: e.ReceivedAt.UTC(), Details: e.Details,
		})
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func (s *AdminServer) handleEmailDeliveries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q, ok := parseAdminQuery(w, r)
	if !ok {
		return
	}
	rows, more, err := s.views.EmailDeliveries(r.Context(), id, q.cursor, q.limit)
	if err != nil {
		s.notFoundOr(w, r, "admin email deliveries", err)
		return
	}
	utc := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		u := t.UTC()
		return &u
	}
	data := make([]map[string]any, 0, len(rows))
	for _, d := range rows {
		data = append(data, map[string]any{
			"id": d.ID.String(), "endpoint_id": d.EndpointID.String(), "endpoint_url": d.EndpointUrl,
			"event_id": d.EventID.String(), "event_type": d.EventType, "status": d.Status, "attempts": d.Attempts,
			"last_status_code": d.LastStatusCode, "last_error_code": d.LastErrorCode,
			"next_attempt_at": utc(d.NextAttemptAt), "delivered_at": utc(d.DeliveredAt),
			"created_at": d.CreatedAt.UTC(), "updated_at": d.UpdatedAt.UTC(),
		})
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func adminTemplateJSON(id, project uuid.UUID, slug, name string, latest int32, created time.Time) map[string]any {
	var lv *int32
	if latest > 0 {
		lv = &latest
	}
	return map[string]any{
		"id": id.String(), "project_id": project.String(), "slug": slug, "name": name,
		"latest_version": lv, "created_at": created.UTC(),
	}
}

func (s *AdminServer) handleTemplates(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAdminQuery(w, r, "project_id")
	if !ok {
		return
	}
	project, ok := q.uuidParam(w, "project_id")
	if !ok {
		return
	}
	rows, more, err := s.views.Templates(r.Context(), project, q.cursor, q.limit)
	if err != nil {
		s.serverError(w, r, "admin list templates", err)
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		data = append(data, adminTemplateJSON(t.ID, t.ProjectID, t.Slug, t.Name, t.LatestVersion, t.CreatedAt))
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func (s *AdminServer) handleTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, versions, err := s.views.Template(r.Context(), id)
	if err != nil {
		s.notFoundOr(w, r, "admin get template", err)
		return
	}
	resp := adminTemplateJSON(t.ID, t.ProjectID, t.Slug, t.Name, t.LatestVersion, t.CreatedAt)
	vs := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		vs = append(vs, map[string]any{"version": v.Version, "created_at": v.CreatedAt.UTC()})
	}
	resp["versions"] = vs
	writeJSON(w, http.StatusOK, resp)
}

// pathVersion parses the {version} path value: a canonical positive integer.
func pathVersion(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.PathValue("version")
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || strconv.Itoa(n) != raw {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return 0, false
	}
	return n, true
}

func (s *AdminServer) handleTemplateVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, ok := pathVersion(w, r)
	if !ok {
		return
	}
	v, err := s.views.TemplateVersion(r.Context(), id, n)
	if err != nil {
		s.notFoundOr(w, r, "admin get template version", err)
		return
	}
	schema, err := templates.ParseSchema(v.Schema)
	if err != nil {
		s.serverError(w, r, "admin get template version: stored schema", errors.New("stored schema unreadable"))
		return
	}
	var html *string
	if v.Source.HTML != "" {
		h := v.Source.HTML
		html = &h
	}
	writeJSONUnescaped(w, http.StatusOK, map[string]any{
		"template_id": v.TemplateID.String(), "project_id": v.ProjectID.String(), "version": v.Version,
		"subject": v.Source.Subject, "text": v.Source.Text, "html": html,
		"variables": json.RawMessage(schema.Canonical()), "created_at": v.CreatedAt.UTC(),
	})
}

type previewRequest struct {
	Variables json.RawMessage `json:"variables"`
}

func (s *AdminServer) handlePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, ok := pathVersion(w, r)
	if !ok {
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var req previewRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	out, err := s.views.Preview(r.Context(), id, n, req.Variables)
	var te *templates.Error
	switch {
	case errors.As(err, &te):
		writeError(w, http.StatusUnprocessableEntity, te.Code, te.Error())
		return
	case err != nil:
		s.notFoundOr(w, r, "admin template preview", err)
		return
	}
	var html *string
	if out.HTML != "" {
		html = &out.HTML
	}
	writeJSONUnescaped(w, http.StatusOK, map[string]any{"subject": out.Subject, "text": out.Text, "html": html})
}

func (s *AdminServer) handleSenders(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAdminQuery(w, r, "project_id")
	if !ok {
		return
	}
	project, ok := q.uuidParam(w, "project_id")
	if !ok {
		return
	}
	rows, more, err := s.views.Senders(r.Context(), project, q.cursor, q.limit)
	if err != nil {
		s.serverError(w, r, "admin list senders", err)
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, si := range rows {
		var verified *time.Time
		if si.VerifiedAt != nil {
			v := si.VerifiedAt.UTC()
			verified = &v
		}
		data = append(data, map[string]any{
			"id": si.ID.String(), "project_id": si.ProjectID.String(), "kind": si.Kind, "value": si.Value,
			"status": si.Status, "verified_at": verified, "created_at": si.CreatedAt.UTC(),
		})
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func (s *AdminServer) handleWebhookEndpoints(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAdminQuery(w, r, "project_id")
	if !ok {
		return
	}
	project, ok := q.uuidParam(w, "project_id")
	if !ok {
		return
	}
	rows, more, err := s.views.WebhookEndpoints(r.Context(), project, q.cursor, q.limit)
	if err != nil {
		s.serverError(w, r, "admin list webhook endpoints", err)
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		data = append(data, map[string]any{
			"id": e.ID.String(), "project_id": e.ProjectID.String(), "url": e.Url, "event_types": e.EventTypes,
			"status": e.Status, "created_at": e.CreatedAt.UTC(), "updated_at": e.UpdatedAt.UTC(),
		})
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

// adminBroadcastJSON mirrors Broadcast in api/admin-openapi.yaml (never the
// shared variables).
func adminBroadcastJSON(b admin.Broadcast) map[string]any {
	resp := map[string]any{
		"id": b.ID.String(), "project_id": b.ProjectID.String(), "project_name": b.ProjectName, "name": b.Name,
		"status": b.Status, "audience_id": b.AudienceID.String(), "from": b.Sender, "template_id": b.TemplateID.String(),
		"template_version": b.TemplateVersion, "unsubscribe_url": b.UnsubscribeUrl,
		"scheduled_at": utcPtr(b.ScheduledAt), "started_at": utcPtr(b.StartedAt), "finished_at": utcPtr(b.FinishedAt),
		"created_at": b.CreatedAt.UTC(),
	}
	if b.ByStatus != nil {
		resp["counts"] = map[string]any{
			"recipients": b.Recipients, "skipped_unsubscribed": b.SkippedUnsubscribed,
			"skipped_suppressed": b.SkippedSuppressed, "skipped_invalid": b.SkippedInvalid, "by_status": b.ByStatus,
		}
	}
	return resp
}

func (s *AdminServer) handleBroadcasts(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAdminQuery(w, r, "project_id", "status")
	if !ok {
		return
	}
	project, ok := q.uuidParam(w, "project_id")
	if !ok {
		return
	}
	var status string
	if v, has := q.values["status"]; has {
		if len(v) != 1 || !broadcasts.IsStatus(v[0]) {
			writeError(w, http.StatusBadRequest, "invalid_filter", "status must be a broadcast status")
			return
		}
		status = v[0]
	}
	rows, more, err := s.views.Broadcasts(r.Context(), project, status, q.cursor, q.limit)
	if err != nil {
		s.serverError(w, r, "admin list broadcasts", err)
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		data = append(data, adminBroadcastJSON(b))
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func (s *AdminServer) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := s.views.Broadcast(r.Context(), id)
	if err != nil {
		s.notFoundOr(w, r, "admin get broadcast", err)
		return
	}
	writeJSON(w, http.StatusOK, adminBroadcastJSON(b))
}

// adminStepJSON mirrors AutomationStep in api/admin-openapi.yaml: a send
// step without its shared variables.
func adminStepJSON(st automations.Step) map[string]any {
	switch st.Type {
	case automations.StepWait:
		return map[string]any{"type": st.Type, "seconds": st.Seconds}
	case automations.StepCondition:
		if st.Event != "" {
			return map[string]any{"type": st.Type, "event": st.Event, "op": st.Op}
		}
		m := map[string]any{"type": st.Type, "attribute": st.Attribute, "op": st.Op}
		if st.Value != nil {
			m["value"] = json.RawMessage(st.Value)
		}
		return m
	}
	return map[string]any{
		"type": st.Type, "from": st.From, "template_id": st.TemplateID.String(), "template_version": st.TemplateVersion,
		"unsubscribe_url": st.UnsubscribeURL,
	}
}

// adminAutomationJSON mirrors Automation in api/admin-openapi.yaml.
func adminAutomationJSON(a admin.Automation) map[string]any {
	resp := map[string]any{
		"id": a.ID.String(), "project_id": a.ProjectID.String(), "project_name": a.ProjectName, "name": a.Name,
		"status": a.Status, "trigger_event": a.TriggerEvent, "allow_reentry": a.AllowReentry, "step_count": a.StepCount,
		"created_at": a.CreatedAt.UTC(), "updated_at": a.UpdatedAt.UTC(),
	}
	if a.RunCounts != nil {
		steps := make([]map[string]any, 0, len(a.Steps))
		for _, st := range a.Steps {
			steps = append(steps, adminStepJSON(st))
		}
		resp["steps"] = steps
		resp["run_counts"] = a.RunCounts
	}
	return resp
}

func adminRunJSON(id, contactID uuid.UUID, status string, reason *string, step int16, next, created, updated, finished *time.Time) map[string]any {
	return map[string]any{
		"id": id.String(), "contact_id": contactID.String(), "status": status, "reason": reason, "step": step,
		"next_step_at": utcPtr(next), "created_at": created.UTC(), "updated_at": updated.UTC(), "finished_at": utcPtr(finished),
	}
}

func (s *AdminServer) handleAutomations(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAdminQuery(w, r, "project_id", "status")
	if !ok {
		return
	}
	project, ok := q.uuidParam(w, "project_id")
	if !ok {
		return
	}
	var status string
	if v, has := q.values["status"]; has {
		if len(v) != 1 || !automations.IsStatus(v[0]) {
			writeError(w, http.StatusBadRequest, "invalid_filter", "status must be an automation status")
			return
		}
		status = v[0]
	}
	rows, more, err := s.views.Automations(r.Context(), project, status, q.cursor, q.limit)
	if err != nil {
		s.serverError(w, r, "admin list automations", err)
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		data = append(data, adminAutomationJSON(a))
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func (s *AdminServer) handleAutomation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := s.views.Automation(r.Context(), id)
	if err != nil {
		s.notFoundOr(w, r, "admin get automation", err)
		return
	}
	writeJSON(w, http.StatusOK, adminAutomationJSON(a))
}

func (s *AdminServer) handleAutomationRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q, ok := parseAdminQuery(w, r, "status")
	if !ok {
		return
	}
	var status string
	if v, has := q.values["status"]; has {
		if len(v) != 1 || !automations.IsRunStatus(v[0]) {
			writeError(w, http.StatusBadRequest, "invalid_filter", "status must be a run status")
			return
		}
		status = v[0]
	}
	rows, more, err := s.views.AutomationRuns(r.Context(), id, status, q.cursor, q.limit)
	if err != nil {
		s.notFoundOr(w, r, "admin list automation runs", err)
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, run := range rows {
		data = append(data, adminRunJSON(run.ID, run.ContactID, run.Status, run.Reason, run.Step, run.NextStepAt, &run.CreatedAt, &run.UpdatedAt, run.FinishedAt))
	}
	var last uuid.UUID
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, listResponse(data, nextCursor(more, last)))
}

func (s *AdminServer) handleAutomationRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	run, err := s.views.AutomationRun(r.Context(), id)
	if err != nil {
		s.notFoundOr(w, r, "admin get automation run", err)
		return
	}
	resp := adminRunJSON(run.ID, run.ContactID, run.Status, run.Reason, run.Step, run.NextStepAt, &run.CreatedAt, &run.UpdatedAt, run.FinishedAt)
	resp["project_id"], resp["project_name"] = run.ProjectID.String(), run.ProjectName
	resp["automation_id"], resp["automation_name"] = run.AutomationID.String(), run.AutomationName
	emails := make([]map[string]any, 0, len(run.Emails))
	for _, e := range run.Emails {
		emails = append(emails, map[string]any{"id": e.ID.String(), "step": e.AutomationStep, "status": e.Status})
	}
	resp["emails"] = emails
	writeJSON(w, http.StatusOK, resp)
}
