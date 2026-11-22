// Package api serves the page and the JSON API described in api/openapi.yaml.
//
// The spec is the source of truth: oapi-codegen generates the types and the
// StrictServerInterface in oapi.gen.go (run `go generate ./...` after editing
// the spec), and every API request is validated against the spec before it
// reaches a handler.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	middleware "github.com/oapi-codegen/nethttp-middleware"

	apispec "github.com/subpathdev/morgentraining/api"
	"github.com/subpathdev/morgentraining/internal/store"
)

const maxBody = 16 << 10

type Server struct {
	Store *store.Store
	Web   fs.FS
	Loc   *time.Location
	Log   *slog.Logger
}

var _ StrictServerInterface = (*Server)(nil)

// LoadSpec parses and checks the embedded OpenAPI description.
func LoadSpec() (*openapi3.T, error) {
	spec, err := openapi3.NewLoader().LoadFromData(apispec.YAML)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec: %w", err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("invalid OpenAPI spec: %w", err)
	}
	return spec, nil
}

func (s *Server) Handler() (http.Handler, error) {
	spec, err := LoadSpec()
	if err != nil {
		return nil, err
	}
	validate := middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
		DoNotValidateServers: true, // the service runs behind a gateway under any host name
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, o middleware.ErrorHandlerOpts) {
			writeJSON(w, o.StatusCode, Error{Error: err.Error()})
		},
	})

	strict := NewStrictHandlerWithOptions(s, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSON(w, http.StatusBadRequest, Error{Error: err.Error()})
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			s.Log.Error("response failed", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusInternalServerError, Error{Error: "internal error"})
		},
	})
	apiMux := http.NewServeMux()
	HandlerFromMux(strict, apiMux)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHeaders(validate(apiMux)))
	mux.Handle("/healthz", apiMux)
	mux.Handle("/openapi.yaml", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(apispec.YAML)
	}))
	mux.Handle("/", s.static())
	return s.logRequests(mux), nil
}

func apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
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

// ---- StrictServerInterface ----

func (s *Server) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	return GetHealth200TextResponse("ok\n"), nil
}

func (s *Server) ListPeople(ctx context.Context, _ ListPeopleRequestObject) (ListPeopleResponseObject, error) {
	people, err := s.Store.People(ctx)
	if err != nil {
		s.Log.Error("list people", "err", err)
		return ListPeople500JSONResponse{internalError()}, nil
	}
	out := make(ListPeople200JSONResponse, len(people))
	for i, p := range people {
		out[i] = toPerson(p)
	}
	return out, nil
}

func (s *Server) GetToday(ctx context.Context, req GetTodayRequestObject) (GetTodayResponseObject, error) {
	date := time.Now().In(s.Loc).Format(store.DateLayout)
	if req.Params.Date != nil {
		date = req.Params.Date.Format(store.DateLayout)
	}
	sess, err := s.Store.Today(ctx, req.Key, date)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return GetToday404JSONResponse{NotFoundJSONResponse{Error: err.Error()}}, nil
	case err != nil:
		s.Log.Error("today", "person", req.Key, "err", err)
		return GetToday500JSONResponse{internalError()}, nil
	}
	return GetToday200JSONResponse(toSession(sess)), nil
}

func (s *Server) AddRound(ctx context.Context, req AddRoundRequestObject) (AddRoundResponseObject, error) {
	in := store.RoundInput{
		PlanVersionID: req.Body.PlanVersionId,
		Date:          req.Body.Date.Format(store.DateLayout),
		Completed:     req.Body.Completed,
	}
	if req.Body.StartedAt != nil {
		in.StartedAt = req.Body.StartedAt.UTC().Format(time.RFC3339)
	}
	if req.Body.SkippedSteps != nil {
		in.SkippedSteps = *req.Body.SkippedSteps
	}
	st, err := s.Store.AddRound(ctx, req.Key, in)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return AddRound404JSONResponse{NotFoundJSONResponse{Error: err.Error()}}, nil
	case errors.Is(err, store.ErrInvalid):
		return AddRound400JSONResponse{BadRequestJSONResponse{Error: err.Error()}}, nil
	case err != nil:
		s.Log.Error("add round", "person", req.Key, "err", err)
		return AddRound500JSONResponse{internalError()}, nil
	}
	return AddRound200JSONResponse(toStats(st)), nil
}

// ---- mapping between the store and the wire format in the spec ----

func internalError() InternalErrorJSONResponse {
	return InternalErrorJSONResponse{Error: "internal error"}
}

func toPerson(p store.Person) Person {
	return Person{Key: p.Key, Name: p.Name, Language: p.Language}
}

func toStats(st store.Stats) Stats {
	return Stats{Streak: st.Streak, TotalDays: st.TotalDays, DoneToday: st.DoneToday}
}

func toSession(s *store.Session) Session {
	out := Session{
		Person: toPerson(s.Person),
		Plan: SessionPlan{Key: s.Plan.Key, Name: s.Plan.Name, Version: s.Plan.Version,
			VersionId: s.Plan.VersionID, TotalSeconds: s.Plan.TotalSeconds},
		Stats:  toStats(s.Stats),
		Blocks: make([]Block, len(s.Blocks)),
	}
	for i, b := range s.Blocks {
		steps := make([]Step, len(b.Steps))
		for j, st := range b.Steps {
			steps[j] = Step{Kind: StepKind(st.Kind), Name: st.Name, Cue: st.Cue, Seconds: st.Seconds}
		}
		out.Blocks[i] = Block{Name: b.Name, Summary: b.Summary, Seconds: b.Seconds, Steps: steps}
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
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
