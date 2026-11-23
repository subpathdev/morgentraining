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
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // the container image has no zoneinfo
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

// run parses the command line and runs one command on an app.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: morgentraining serve|import|backup [flags] (see -h of each)")
		return errUsage
	}
	cmd, args := args[0], args[1:]
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(stderr)
	dbPath := fl.String("db", env("MT_DB", "morgentraining.db"), "SQLite file (MT_DB)")

	var command func(*app) error
	switch cmd {
	case "serve":
		addr := fl.String("addr", env("MT_ADDR", ":8080"), "listen address (MT_ADDR)")
		command = func(a *app) error { return a.serve(ctx, *addr) }
	case "import":
		command = func(a *app) error {
			if fl.NArg() == 0 {
				return errors.New("import: name at least one YAML file")
			}
			return a.importFiles(ctx, fl.Args())
		}
	case "backup":
		command = func(a *app) error {
			if fl.NArg() != 1 {
				return errors.New("backup: name one target file")
			}
			return a.backup(ctx, fl.Arg(0))
		}
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", cmd)
		return errUsage
	}
	if err := fl.Parse(args); err != nil {
		return errUsage
	}
	a, err := newApp(config{
		DBPath:   *dbPath,
		TimeZone: env("MT_TZ", "Europe/Berlin"),
		Stdout:   stdout,
		Stderr:   stderr,
	})
	if err != nil {
		return err
	}
	defer a.Close()
	return command(a)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
