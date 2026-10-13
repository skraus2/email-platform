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
	"github.com/skraus2/email-platform/internal/store"
)

const usage = `usage:
  admin project create -name <name>
  admin project list
  admin project disable -id <uuid>
  admin project enable -id <uuid>
  admin key create -project <uuid> [-scopes emails:send,emails:read]
  admin key list -project <uuid>
  admin key revoke -prefix <prefix>`

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
	id := fs.String("id", "", "project id")
	project := fs.String("project", "", "project id")
	scopes := fs.String("scopes", "", "comma-separated scopes (default: all)")
	prefix := fs.String("prefix", "", "key prefix")
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
	default:
		return errors.New(usage)
	}
	return nil
}
