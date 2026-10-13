// Command migrate applies or inspects the embedded database migrations.
//
// Usage: migrate up | migrate status
//
// There is intentionally no "down" command: rollbacks are planned per change
// and run deliberately, never as a routine CLI action.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/database"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || (args[0] != "up" && args[0] != "status") {
		return fmt.Errorf("usage: migrate up|status")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	m, err := database.NewMigrator(pool)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	switch args[0] {
	case "up":
		results, err := m.Up(ctx)
		if err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		for _, r := range results {
			fmt.Printf("applied %s (%s)\n", r.Source.Path, r.Duration)
		}
		current, _, err := m.Versions(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("database at version %d (%d applied now)\n", current, len(results))
	case "status":
		statuses, err := m.Status(ctx)
		if err != nil {
			return fmt.Errorf("read migration status: %w", err)
		}
		for _, s := range statuses {
			fmt.Printf("%-8s %s\n", s.State, s.Source.Path)
		}
	}
	return nil
}
