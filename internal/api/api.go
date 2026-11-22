// Package api serves the page and the JSON API.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/subpathdev/morgentraining/internal/store"
)

type Server struct {
	Store *store.Store
	Web   fs.FS
	Loc   *time.Location
	Log   *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /api/people", s.people)
	mux.HandleFunc("GET /api/people/{key}/today", s.today)
	mux.HandleFunc("POST /api/people/{key}/rounds", s.addRound)
	mux.Handle("GET /", s.static())
	return s.logRequests(mux)
}

func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.Web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The page is small and changes with each release; always revalidate.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		files.ServeHTTP(w, r)
	})
}

func (s *Server) todayDate(r *http.Request) (string, error) {
	if d := r.URL.Query().Get("date"); d != "" {
		if _, err := time.Parse(store.DateLayout, d); err != nil {
			return "", errors.New("date must be YYYY-MM-DD")
		}
		return d, nil
	}
	return time.Now().In(s.Loc).Format(store.DateLayout), nil
}

func (s *Server) people(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.People(r.Context())
	s.reply(w, r, p, err)
}

func (s *Server) today(w http.ResponseWriter, r *http.Request) {
	date, err := s.todayDate(r)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	sess, err := s.Store.Today(r.Context(), r.PathValue("key"), date)
	s.reply(w, r, sess, err)
}

func (s *Server) addRound(w http.ResponseWriter, r *http.Request) {
	var in store.RoundInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.Store.AddRound(r.Context(), r.PathValue("key"), in)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	s.reply(w, r, st, err)
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, v any, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, http.StatusNotFound, err)
	case err != nil:
		s.Log.Error("request failed", "path", r.URL.Path, "err", err)
		s.fail(w, http.StatusInternalServerError, errors.New("internal error"))
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(v)
	}
}

func (s *Server) fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" {
			s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}
