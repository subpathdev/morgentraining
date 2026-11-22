// Command morgentraining serves the morning workout page and keeps plans and rounds in SQLite.
//
//	morgentraining serve  [-addr :8080] [-db morgentraining.db]
//	morgentraining import [-db morgentraining.db] FILE.yaml...
//	morgentraining backup [-db morgentraining.db] TARGET.db
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the container image has no zoneinfo

	"github.com/subpathdev/morgentraining/internal/api"
	"github.com/subpathdev/morgentraining/internal/store"
	"github.com/subpathdev/morgentraining/seed"
	"github.com/subpathdev/morgentraining/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(log, os.Args[2:])
	case "import":
		err = importFiles(os.Args[2:])
	case "backup":
		err = backup(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: morgentraining serve|import|backup [flags] (see -h of each)")
	os.Exit(2)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func location() (*time.Location, error) {
	return time.LoadLocation(env("MT_TZ", "Europe/Berlin"))
}

func serve(log *slog.Logger, args []string) error {
	fl := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fl.String("addr", env("MT_ADDR", ":8080"), "listen address (MT_ADDR)")
	dbPath := fl.String("db", env("MT_DB", "morgentraining.db"), "SQLite file (MT_DB)")
	fl.Parse(args)

	loc, err := location()
	if err != nil {
		return err
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := seedIfEmpty(st, loc, log); err != nil {
		return err
	}

	handler, err := (&api.Server{Store: st, Web: web.Files, Loc: loc, Log: log}).Handler()
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Info("listening", "addr", *addr, "db", *dbPath, "tz", loc.String())
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// seedIfEmpty loads the embedded plans, then people, into a database without plans.
func seedIfEmpty(st *store.Store, loc *time.Location, log *slog.Logger) error {
	ctx := context.Background()
	empty, err := st.Empty(ctx)
	if err != nil || !empty {
		return err
	}
	names, err := fs.Glob(seed.Files, "*.yaml")
	if err != nil {
		return err
	}
	// Plans before people, because people refer to plans.
	sort.Slice(names, func(i, j int) bool {
		pi, pj := strings.HasPrefix(names[i], "plan"), strings.HasPrefix(names[j], "plan")
		if pi != pj {
			return pi
		}
		return names[i] < names[j]
	})
	today := time.Now().In(loc).Format(store.DateLayout)
	for _, n := range names {
		data, err := fs.ReadFile(seed.Files, n)
		if err != nil {
			return err
		}
		res, err := st.Import(ctx, data, today)
		if err != nil {
			return fmt.Errorf("seed %s: %w", n, err)
		}
		log.Info("seeded", "file", n, "kind", res.Kind, "plan", res.Plan, "version", res.Version)
	}
	return nil
}

func importFiles(args []string) error {
	fl := flag.NewFlagSet("import", flag.ExitOnError)
	dbPath := fl.String("db", env("MT_DB", "morgentraining.db"), "SQLite file (MT_DB)")
	fl.Parse(args)
	if fl.NArg() == 0 {
		return errors.New("import: name at least one YAML file")
	}
	loc, err := location()
	if err != nil {
		return err
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	today := time.Now().In(loc).Format(store.DateLayout)
	for _, f := range fl.Args() {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		res, err := st.Import(context.Background(), data, today)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		switch {
		case res.Kind == "people":
			fmt.Printf("%s: %d people saved\n", f, res.People)
		case res.Created:
			fmt.Printf("%s: plan %s saved as version %d\n", f, res.Plan, res.Version)
		default:
			fmt.Printf("%s: plan %s unchanged (version %d)\n", f, res.Plan, res.Version)
		}
	}
	return nil
}

func backup(args []string) error {
	fl := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fl.String("db", env("MT_DB", "morgentraining.db"), "SQLite file (MT_DB)")
	fl.Parse(args)
	if fl.NArg() != 1 {
		return errors.New("backup: name one target file")
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Backup(context.Background(), fl.Arg(0))
}
