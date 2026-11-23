package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/subpathdev/morgentraining/internal/httpapi"
	"github.com/subpathdev/morgentraining/internal/importer"
	"github.com/subpathdev/morgentraining/internal/sqlite"
	"github.com/subpathdev/morgentraining/internal/training"
	"github.com/subpathdev/morgentraining/seed"
	"github.com/subpathdev/morgentraining/web"
)

type config struct {
	DBPath   string
	TimeZone string
	Stdout   io.Writer
	Stderr   io.Writer
	Now      func() time.Time // nil means time.Now
}

// app holds the wired-up parts. Each command is a method on it.
type app struct {
	store    sqlite.DB
	svc      *training.Service
	importer *importer.Importer
	log      *slog.Logger
	stdout   io.Writer
}

func newApp(cfg config) (*app, error) {
	loc, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		return nil, fmt.Errorf("time zone: %w", err)
	}
	store, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	var opts []training.Option
	if cfg.Now != nil {
		opts = append(opts, training.WithClock(cfg.Now))
	}
	svc := training.NewService(store, loc, opts...)
	log := slog.New(slog.NewTextHandler(cfg.Stderr, nil))
	return &app{store: store, svc: svc, importer: importer.New(svc, log), log: log, stdout: cfg.Stdout}, nil
}

func (a *app) Close() error { return a.store.Close() }

// handler seeds an empty database and returns the HTTP handler.
func (a *app) handler(ctx context.Context) (http.Handler, error) {
	if err := a.importer.SeedIfEmpty(ctx, seed.Files); err != nil {
		return nil, err
	}
	return httpapi.New(a.svc, httpapi.Options{Static: web.Files, Logger: a.log})
}

// serve runs the HTTP server until ctx is cancelled, then shuts it down gracefully.
func (a *app) serve(ctx context.Context, addr string) error {
	handler, err := a.handler(ctx)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	a.log.Info("listening", "addr", addr)

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

func (a *app) importFiles(ctx context.Context, names []string) error {
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		res, err := a.importer.File(ctx, data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(a.stdout, "%s: %s\n", name, res)
	}
	return nil
}

func (a *app) backup(ctx context.Context, target string) error {
	return a.store.Backup(ctx, target)
}
