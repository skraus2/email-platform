package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/usage"
)

// UsageService reads a project's daily usage.
type UsageService interface {
	Days(ctx context.Context, projectID uuid.UUID, from, to time.Time) ([]usage.Day, error)
}

// UsageLimits are the per-project limits GET /v1/usage reports.
type UsageLimits struct {
	SendsPerMinute         int
	ContactEventsPerMinute int
}

// usageCounts mirrors UsageCounts in api/openapi.yaml.
type usageCounts struct {
	Accepted   int64 `json:"accepted"`
	Sent       int64 `json:"sent"`
	Failed     int64 `json:"failed"`
	Canceled   int64 `json:"canceled"`
	Delivered  int64 `json:"delivered"`
	Bounced    int64 `json:"bounced"`
	Complained int64 `json:"complained"`
}

type usageDay struct {
	Date string `json:"date"`
	usageCounts
}

type usageResponse struct {
	From   string      `json:"from"`
	To     string      `json:"to"`
	Data   []usageDay  `json:"data"`
	Totals usageCounts `json:"totals"`
	Limits struct {
		SendsPerMinute         int `json:"sends_per_minute"`
		ContactEventsPerMinute int `json:"contact_events_per_minute"`
	} `json:"limits"`
}

func toUsageCounts(c usage.Counts) usageCounts {
	return usageCounts{
		Accepted: c.Accepted, Sent: c.Sent, Failed: c.Failed, Canceled: c.Canceled,
		Delivered: c.Delivered, Bounced: c.Bounced, Complained: c.Complained,
	}
}

// defaultUsageDays is the range GET /v1/usage covers without from.
const defaultUsageDays = 30

var usageParams = map[string]bool{"from": true, "to": true}

// parseUsageRange reads from and to (UTC dates, both inclusive) with their
// defaults relative to now. msg names the offending parameter, never its
// value; "" means valid.
func parseUsageRange(query url.Values, now time.Time) (from, to time.Time, msg string) {
	for k := range query {
		if !usageParams[k] {
			return from, to, "unknown query parameter"
		}
	}
	date := func(name string) (time.Time, bool, string) {
		v, ok := query[name]
		if !ok {
			return time.Time{}, false, ""
		}
		d, err := time.Parse(time.DateOnly, v[0])
		if len(v) != 1 || err != nil {
			return time.Time{}, false, name + " must be a single date (YYYY-MM-DD)"
		}
		return d, true, ""
	}
	to, hasTo, msg := date("to")
	if msg != "" {
		return from, to, msg
	}
	if !hasTo {
		to = now.UTC().Truncate(24 * time.Hour)
	}
	from, hasFrom, msg := date("from")
	if msg != "" {
		return from, to, msg
	}
	if !hasFrom {
		from = to.AddDate(0, 0, -(defaultUsageDays - 1))
	}
	switch {
	case from.After(to):
		return from, to, "from must not be after to"
	case to.Sub(from) >= usage.MaxDays*24*time.Hour:
		return from, to, "from and to may cover at most " + strconv.Itoa(usage.MaxDays) + " days (both included)"
	}
	return from, to, ""
}

func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return
	}
	from, to, msg := parseUsageRange(query, time.Now())
	if msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_filter", msg)
		return
	}
	p := principalFrom(r.Context())
	days, err := s.usage.Days(r.Context(), p.ProjectID, from, to)
	switch {
	case database.Unavailable(err):
		s.logger.ErrorContext(r.Context(), "get usage: database unavailable",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeUnavailable(w)
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "get usage failed",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	resp := usageResponse{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Data: make([]usageDay, 0, len(days))}
	var total usage.Counts
	for _, d := range days {
		resp.Data = append(resp.Data, usageDay{Date: d.Date.Format(time.DateOnly), usageCounts: toUsageCounts(d.Counts)})
		total = total.Add(d.Counts)
	}
	resp.Totals = toUsageCounts(total)
	resp.Limits.SendsPerMinute = s.usageLimits.SendsPerMinute
	resp.Limits.ContactEventsPerMinute = s.usageLimits.ContactEventsPerMinute
	writeJSON(w, http.StatusOK, resp)
}
