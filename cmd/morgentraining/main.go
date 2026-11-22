// Command morgentraining serves the morning workout page and keeps plans and rounds in SQLite.
//
//	morgentraining serve  [-addr :8080] [-db morgentraining.db]
//	morgentraining import [-db morgentraining.db] FILE.yaml...
//	morgentraining backup [-db morgentraining.db] TARGET.db
//
// Settings can also come from the environment: MT_ADDR, MT_DB and MT_TZ
// (the time zone that decides which day "today" is, default Europe/Berlin).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // the container image has no zoneinfo

	"github.com/subpathdev/morgentraining/internal/httpapi"
	"github.com/subpathdev/morgentraining/internal/sqlite"
	"github.com/subpathdev/morgentraining/internal/training"
	"github.com/subpathdev/morgentraining/seed"
	"github.com/subpathdev/morgentraining/web"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if errors.Is(err, errUsage) {
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: morgentraining serve|import|backup [flags] (see -h of each)")
		return errUsage
	}
	cmd, args := args[0], args[1:]
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(stderr)
	dbPath := fl.String("db", env("MT_DB", "morgentraining.db"), "SQLite file (MT_DB)")

	switch cmd {
	case "serve":
		addr := fl.String("addr", env("MT_ADDR", ":8080"), "listen address (MT_ADDR)")
		if err := fl.Parse(args); err != nil {
			return errUsage
		}
		return withService(*dbPath, func(db *sqlite.DB, svc *training.Service) error {
			log := slog.New(slog.NewTextHandler(stderr, nil))
			if err := seedIfEmpty(ctx, svc, seed.Files, log); err != nil {
				return err
			}
			return serve(ctx, *addr, svc, log)
		})
	case "import":
		if err := fl.Parse(args); err != nil {
			return errUsage
		}
		if fl.NArg() == 0 {
			return errors.New("import: name at least one YAML file")
		}
		return withService(*dbPath, func(_ *sqlite.DB, svc *training.Service) error {
			for _, name := range fl.Args() {
				data, err := os.ReadFile(name)
				if err != nil {
					return err
				}
				msg, err := importFile(ctx, svc, data)
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				fmt.Fprintf(stdout, "%s: %s\n", name, msg)
			}
			return nil
		})
	case "backup":
		if err := fl.Parse(args); err != nil {
			return errUsage
		}
		if fl.NArg() != 1 {
			return errors.New("backup: name one target file")
		}
		return withService(*dbPath, func(db *sqlite.DB, _ *training.Service) error {
			return db.Backup(ctx, fl.Arg(0))
		})
	}
	fmt.Fprintf(stderr, "unknown command %q\n", cmd)
	return errUsage
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// withService opens the database, builds the service and closes the database afterwards.
func withService(dbPath string, fn func(*sqlite.DB, *training.Service) error) error {
	loc, err := time.LoadLocation(env("MT_TZ", "Europe/Berlin"))
	if err != nil {
		return fmt.Errorf("MT_TZ: %w", err)
	}
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(db, training.NewService(db, loc))
}

func serve(ctx context.Context, addr string, svc *training.Service, log *slog.Logger) error {
	handler, err := httpapi.New(svc, httpapi.Options{Static: web.Files, Logger: log})
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
