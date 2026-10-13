package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/skraus2/email-platform/internal/auth"
)

// Authenticator verifies a bearer token and returns its principal, or
// auth.ErrUnauthorized. Any other error means the check itself failed.
type Authenticator func(ctx context.Context, token string) (auth.Principal, error)

type principalKey struct{}

// principalFrom returns the authenticated principal. Only valid inside
// handlers wrapped by requireScope.
func principalFrom(ctx context.Context) auth.Principal {
	p, _ := ctx.Value(principalKey{}).(auth.Principal)
	return p
}

// requireScope authenticates the request with a project API key and checks
// scope before calling next. The token and header are never logged.
func (s *Server) requireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w)
			return
		}
		p, err := s.authenticate(r.Context(), token)
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			unauthorized(w)
			return
		case err != nil:
			s.logger.ErrorContext(r.Context(), "authentication backend failed", slog.Any("error", err))
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "service temporarily unavailable")
			return
		}
		setLogProject(r.Context(), p.ProjectID.String())
		if !p.Has(scope) {
			writeError(w, http.StatusForbidden, "forbidden", "api key lacks scope "+scope)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

// bearerToken extracts the token from "Bearer <token>" (scheme
// case-insensitive, exactly one space, no surrounding whitespace).
func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="email-platform"`)
	writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid api key")
}
