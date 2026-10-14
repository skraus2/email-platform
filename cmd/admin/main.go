// Command admin manages projects and API keys directly in the database.
// It is the internal admin path; project API keys can never do this.
//
//	admin project create -name <name>
//	admin project list
//	admin project disable -id <uuid>
//	admin project enable -id <uuid>
//	admin key create -project <uuid> [-scopes emails:send,emails:read]
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
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/store"
)

const usage = `usage:
  admin project create -name <name>
  admin project list
  admin project disable -id <uuid>
  admin project enable -id <uuid>
  admin key create -project <uuid> [-scopes emails:send,emails:read]
  admin key list -project <uuid>
  admin key revoke -prefix <prefix>
  admin email list -project <uuid> [-status failed] [-limit 50]
  admin suppression add -project <uuid> -email <addr> [-reason manual]
  admin suppression list -project <uuid> [-limit 50]
  admin suppression remove -project <uuid> -email <addr>
  admin sender add -project <uuid> (-address <addr> | -domain <domain>)
  admin sender list -project <uuid> [-limit 50]
  admin sender verify -project <uuid> -id <uuid>
  admin sender remove -project <uuid> -id <uuid>`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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
	id := fs.String("id", "", "project or sender identity id")
	project := fs.String("project", "", "project id")
	scopes := fs.String("scopes", "", "comma-separated scopes (default: all)")
	prefix := fs.String("prefix", "", "key prefix")
	status := fs.String("status", "", "only emails with this status")
	limit := fs.Int("limit", emails.DefaultAdminListLimit, "maximum number of emails")
	email := fs.String("email", "", "email address")
	address := fs.String("address", "", "sender address")
	domain := fs.String("domain", "", "sender domain")
	reason := fs.String("reason", "manual", "suppression reason: "+strings.Join(emails.SuppressionReasons, ", "))
	if err := fs.Parse(args[2:]); err != nil {
		return err
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
		return w.Flush()
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
		return w.Flush()
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
		return w.Flush()
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
	default:
		return errors.New(usage)
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
	return w.Flush()
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
		return w.Flush()
	}
}
