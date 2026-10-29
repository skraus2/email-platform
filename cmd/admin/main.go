// Command admin manages projects and API keys directly in the database.
// It is the internal admin path; project API keys can never do this.
//
//	admin project create -name <name>
//	admin project list
//	admin project disable -id <uuid>
//	admin project enable -id <uuid>
//	admin key create -project <uuid> [-scopes emails:send,emails:read,templates:read,templates:write,webhooks:write,contacts:read,contacts:write,broadcasts:read,broadcasts:write,automations:read,automations:write,events:write]
//	admin key list -project <uuid>
//	admin key revoke -prefix <prefix>
//	admin email list -project <uuid> [-status failed] [-limit 50]
//	admin suppression add -project <uuid> -email <addr> [-reason manual]
//	admin suppression list -project <uuid> [-limit 50]
//	admin suppression remove -project <uuid> -email <addr>
//	admin sender add -project <uuid> (-address <addr> | -domain <domain>)
//	admin sender list -project <uuid> [-limit 50]
//	admin sender verify -project <uuid> -id <uuid>
//	admin sender remove -project <uuid> -id <uuid>
//	admin webhook create -project <uuid> -url <url> [-events email.bounced,…]
//	admin webhook list -project <uuid>
//	admin webhook disable|enable -project <uuid> -id <uuid>
//	admin webhook rotate-secret -project <uuid> -id <uuid>
//	admin webhook reencrypt
//	admin webhook deliveries -project <uuid> [-status failed] [-limit 50]
//	admin user create|set-password -username <name>   (password from stdin)
//	admin user list
//	admin user disable|enable|unlock -username <name>
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	adminacct "github.com/skraus2/email-platform/internal/admin"
	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/webhooks"
)

const usage = `usage:
  admin project create -name <name>
  admin project list
  admin project disable -id <uuid>
  admin project enable -id <uuid>
  admin key create -project <uuid> [-scopes emails:send,emails:read,templates:read,templates:write,webhooks:write,contacts:read,contacts:write,broadcasts:read,broadcasts:write,automations:read,automations:write,events:write]
  admin key list -project <uuid>
  admin key revoke -prefix <prefix>
  admin email list -project <uuid> [-status failed] [-limit 50]
  admin suppression add -project <uuid> -email <addr> [-reason manual]
  admin suppression list -project <uuid> [-limit 50]
  admin suppression remove -project <uuid> -email <addr>
  admin sender add -project <uuid> (-address <addr> | -domain <domain>)
  admin sender list -project <uuid> [-limit 50]
  admin sender verify -project <uuid> -id <uuid>
  admin sender remove -project <uuid> -id <uuid>
  admin webhook create -project <uuid> -url <url> [-events email.bounced,…]
  admin webhook list -project <uuid>
  admin webhook disable|enable -project <uuid> -id <uuid>
  admin webhook rotate-secret -project <uuid> -id <uuid>
  admin webhook reencrypt
  admin webhook deliveries -project <uuid> [-status failed] [-limit 50]
  admin user create -username <name>        (password from stdin)
  admin user set-password -username <name>  (password from stdin)
  admin user list
  admin user disable|enable|unlock -username <name>`

func main() {
	os.Exit(exitCode())
}

// exitCode runs the command; deferred cleanup finishes before main exits.
func exitCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		return 1
	}
	return 0
}

// flush writes out a buffered table.
func flush(w *tabwriter.Writer) error {
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return errors.New(usage)
	}
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	q := store.New(pool)

	fs := flag.NewFlagSet(args[0]+" "+args[1], flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "project name")
	id := fs.String("id", "", "project, sender identity or webhook endpoint id")
	project := fs.String("project", "", "project id")
	scopes := fs.String("scopes", "", "comma-separated scopes (default: emails:send,emails:read)")
	prefix := fs.String("prefix", "", "key prefix")
	status := fs.String("status", "", "only emails with this status")
	limit := fs.Int("limit", emails.DefaultAdminListLimit, "maximum number of emails")
	email := fs.String("email", "", "email address")
	address := fs.String("address", "", "sender address")
	hookURL := fs.String("url", "", "webhook URL")
	hookEvents := fs.String("events", "", "comma-separated webhook event types (default: all)")
	domain := fs.String("domain", "", "sender domain")
	username := fs.String("username", "", "admin user name")
	reason := fs.String("reason", "manual", "suppression reason: "+strings.Join(emails.SuppressionReasons, ", "))
	if err := fs.Parse(args[2:]); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	switch args[0] + " " + args[1] {
	case "project create":
		p, err := auth.CreateProject(ctx, q, *name)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\t%s\n", p.ID, p.Name)
	case "project list":
		ps, err := q.ListProjects(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCREATED")
		for _, p := range ps {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Status, p.CreatedAt.Format(time.RFC3339))
		}
		return flush(w)
	case "project disable", "project enable":
		pid, err := uuid.Parse(*id)
		if err != nil {
			return errors.New("-id must be a project uuid")
		}
		return auth.SetProjectStatus(ctx, q, pid, args[1] == "enable")
	case "key create":
		pid, err := uuid.Parse(*project)
		if err != nil {
			return errors.New("-project must be a project uuid")
		}
		var sc []string
		if *scopes != "" {
			sc = strings.Split(*scopes, ",")
		}
		token, key, err := auth.CreateKey(ctx, q, pid, sc)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "Created key %s with scopes %s. Store it now; it cannot be shown again.\n",
			key.Prefix, strings.Join(key.Scopes, ","))
		fmt.Fprintln(stdout, token)
	case "key list":
		pid, err := uuid.Parse(*project)
		if err != nil {
			return errors.New("-project must be a project uuid")
		}
		keys, err := q.ListAPIKeys(ctx, pid)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PREFIX\tSCOPES\tCREATED\tREVOKED")
		for _, k := range keys {
			revoked := "-"
			if k.RevokedAt != nil {
				revoked = k.RevokedAt.Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.Prefix, strings.Join(k.Scopes, ","), k.CreatedAt.Format(time.RFC3339), revoked)
		}
		return flush(w)
	case "key revoke":
		return auth.RevokeKey(ctx, q, *prefix)
	case "email list":
		pid, err := uuid.Parse(*project)
		if err != nil {
			return errors.New("-project must be a project uuid")
		}
		list, err := emails.ListEmails(ctx, q, pid, *status, *limit)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tERROR_CODE\tDETAIL\tATTEMPTS\tRECIPIENT\tCREATED\tUPDATED")
		for _, e := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", e.ID, e.Status, orDash(e.ErrorCode), orDash(e.Detail),
				e.Attempts, e.Recipient, e.CreatedAt.Format(time.RFC3339), e.UpdatedAt.Format(time.RFC3339))
		}
		return flush(w)
	case "suppression add", "suppression list", "suppression remove":
		pid, err := uuid.Parse(*project)
		if err != nil {
			return errors.New("-project must be a project uuid")
		}
		return suppression(ctx, q, args[1], pid, *email, *reason, *limit, stdout)
	case "sender add", "sender list":
		pid, err := uuid.Parse(*project)
		if err != nil {
			return errors.New("-project must be a project uuid")
		}
		if args[1] == "list" {
			return listSenders(ctx, q, pid, *limit, stdout)
		}
		var kind emails.SenderKind
		var value string
		switch {
		case *address != "" && *domain == "":
			kind, value = emails.SenderAddress, *address
		case *domain != "" && *address == "":
			kind, value = emails.SenderDomain, *domain
		default:
			return errors.New("give exactly one of -address or -domain")
		}
		s, err := emails.AddSender(ctx, q, pid, kind, value)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", s.ID, s.Kind, emails.TerminalSafe(s.Value), s.Status)
	case "sender verify", "sender remove":
		pid, err := uuid.Parse(*project)
		if err != nil {
			return errors.New("-project must be a project uuid")
		}
		sid, err := uuid.Parse(*id)
		if err != nil {
			return errors.New("-id must be a sender identity uuid")
		}
		if args[1] == "verify" {
			ok, err := emails.VerifySender(ctx, q, pid, sid)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("no pending sender identity with this id in this project")
			}
			return nil
		}
		ok, err := emails.RemoveSender(ctx, q, pid, sid)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no sender identity with this id in this project")
		}
	case "webhook reencrypt":
		n, err := webhooks.Reencrypt(ctx, pool, cfg.WebhookKeys)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "re-encrypted %d secrets with key %s\n", n, cfg.WebhookKeys.ActiveKeyID())
	case "webhook deliveries":
		pid, err := uuid.Parse(*project)
		if err != nil {
			return errors.New("-project must be a project uuid")
		}
		return listDeliveries(ctx, q, pid, *status, *limit, stdout)
	case "webhook create", "webhook list", "webhook disable", "webhook enable", "webhook rotate-secret":
		pid, err := uuid.Parse(*project)
		if err != nil {
			return errors.New("-project must be a project uuid")
		}
		return webhook(ctx, cfg, pool, q, args[1], pid, *id, *hookURL, *hookEvents, stdout)
	case "user create", "user set-password", "user list", "user disable", "user enable", "user unlock":
		return userCommand(ctx, adminacct.NewService(pool, slog.New(slog.DiscardHandler), cfg.AdminSessionIdle, cfg.AdminSessionMax),
			args[1], *username, stdin, stdout)
	default:
		return errors.New(usage)
	}
	return nil
}

func webhook(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, q *store.Queries, action string, pid uuid.UUID, id, rawURL, events string, stdout io.Writer) error {
	switch action {
	case "create":
		var types []string
		if events != "" {
			types = strings.Split(events, ",")
		}
		reg := webhooks.Registrar{Pool: pool, Keys: cfg.WebhookKeys, Policy: cfg.WebhookTargets, MaxEndpoints: cfg.WebhookMaxEndpointsPerProject}
		e, secret, err := reg.Create(ctx, pid, rawURL, types)
		if errors.Is(err, webhooks.ErrEndpointLimit) {
			return fmt.Errorf("%w (WEBHOOK_MAX_ENDPOINTS_PER_PROJECT=%d; disabled endpoints count): reuse an endpoint with `admin webhook rotate-secret` or raise the limit",
				err, cfg.WebhookMaxEndpointsPerProject)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\t%s\n", e.ID, strings.Join(e.EventTypes, ","))
		fmt.Fprintf(stdout, "secret (shown once, store it now): %s\n", secret)
		return nil
	case "list":
		list, err := webhooks.ListEndpoints(ctx, q, pid)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tEVENTS\tKEY\tURL")
		for _, e := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID, e.Status, strings.Join(e.EventTypes, ","), e.KeyID, emails.TerminalSafe(e.URL))
		}
		return flush(w)
	}
	eid, err := uuid.Parse(id)
	if err != nil {
		return errors.New("-id must be a webhook endpoint uuid")
	}
	if action == "rotate-secret" {
		secret, err := webhooks.RotateSecret(ctx, pool, cfg.WebhookKeys, pid, eid)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "new secret (shown once; the old one stops working now): %s\n", secret)
		return nil
	}
	changed, err := webhooks.SetEndpointActive(ctx, pool, pid, eid, action == "enable")
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(stdout, "already %sd\n", action)
	}
	return nil
}

func listSenders(ctx context.Context, q *store.Queries, pid uuid.UUID, limit int, stdout io.Writer) error {
	list, err := emails.ListSenders(ctx, q, pid, limit)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tVALUE\tSTATUS\tVERIFIED")
	for _, s := range list {
		verified := "-"
		if s.VerifiedAt != nil {
			verified = s.VerifiedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Kind, emails.TerminalSafe(s.Value), s.Status, verified)
	}
	return flush(w)
}

func listDeliveries(ctx context.Context, q *store.Queries, pid uuid.UUID, status string, limit int, stdout io.Writer) error {
	list, err := webhooks.ListDeliveries(ctx, q, pid, status, limit)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tENDPOINT\tEMAIL\tEVENT\tSTATUS\tATTEMPTS\tHTTP\tERROR\tNEXT ATTEMPT")
	for _, d := range list {
		httpStatus, next := "-", "-"
		if d.LastStatusCode != nil {
			httpStatus = fmt.Sprint(*d.LastStatusCode)
		}
		if d.NextAttemptAt != nil {
			next = d.NextAttemptAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\temail.%s\t%s\t%d\t%s\t%s\t%s\n",
			d.ID, d.EndpointID, d.EmailID, d.EventType, d.Status, d.Attempts, httpStatus, orDash(d.LastErrorCode), next)
	}
	return flush(w)
}

func orDash(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

func suppression(ctx context.Context, q *store.Queries, action string, pid uuid.UUID, email, reason string, limit int, stdout io.Writer) error {
	switch action {
	case "add":
		added, err := emails.AddSuppression(ctx, q, pid, email, reason)
		if err != nil {
			return err
		}
		if !added {
			fmt.Fprintln(stdout, "already suppressed")
		}
		return nil
	case "remove":
		removed, err := emails.RemoveSuppression(ctx, q, pid, email)
		if err != nil {
			return err
		}
		if !removed {
			fmt.Fprintln(stdout, "not suppressed")
		}
		return nil
	default:
		list, err := emails.ListSuppressions(ctx, q, pid, limit)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "RECIPIENT\tREASON\tCREATED")
		for _, s := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\n", emails.TerminalSafe(s.Recipient), s.Reason, s.CreatedAt.Format(time.RFC3339))
		}
		return flush(w)
	}
}

// userCommand manages dashboard accounts. Passwords are read from stdin
// (first line), never from arguments or the environment.
func userCommand(ctx context.Context, svc *adminacct.Service, cmd, username string, stdin io.Reader, stdout io.Writer) error {
	if cmd == "list" {
		users, err := svc.ListUsers(ctx)
		if err != nil {
			return err
		}
		for _, u := range users {
			state := "enabled"
			switch {
			case u.Disabled:
				state = "disabled"
			case u.Locked:
				state = "locked"
			}
			fmt.Fprintf(stdout, "%s\t%s\tcreated %s\tpassword set %s\n", u.Username, state,
				u.CreatedAt.UTC().Format(time.RFC3339), u.PasswordSetAt.UTC().Format(time.RFC3339))
		}
		return nil
	}
	if username == "" {
		return errors.New("-username is required")
	}
	readPassword := func() (string, error) {
		line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	switch cmd {
	case "create":
		pw, err := readPassword()
		if err != nil {
			return err
		}
		if _, err := svc.CreateUser(ctx, username, pw); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Created admin user %s.\n", username)
	case "set-password":
		pw, err := readPassword()
		if err != nil {
			return err
		}
		if err := svc.SetPassword(ctx, username, pw); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Password of %s changed; their sessions ended.\n", username)
	case "disable", "enable":
		if err := svc.SetDisabled(ctx, username, cmd == "disable"); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Admin user %s %sd.\n", username, cmd)
	case "unlock":
		if err := svc.Unlock(ctx, username); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Admin user %s unlocked.\n", username)
	}
	return nil
}
