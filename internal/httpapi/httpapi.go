// Package httpapi serves the page and the JSON API described in api/openapi.yaml.
//
// The spec is the source of truth: the wire types and the typed server interface
// are generated into package oapi, and every API request is validated against the
// spec before it reaches a handler. This package only translates between the wire
// format and the training domain.
package httpapi

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
	"github.com/subpathdev/morgentraining/internal/httpapi/oapi"
	"github.com/subpathdev/morgentraining/internal/training"
)

const maxBody = 16 << 10

// Trainer is what the handlers need from the domain. *training.Service implements it.
type Trainer interface {
	Today() training.Date
	People(ctx context.Context) ([]training.Person, error)
	Session(ctx context.Context, personKey string, day training.Date) (training.Session, error)
	RecordRound(ctx context.Context, personKey string, in training.RoundInput) (training.Stats, error)
}

// PeopleEditor is what the handlers need to add or change people.
type PeopleEditor interface {
	// SavePerson adds or updates a person and reports whether they are new.
	SavePerson(ctx context.Context, p training.PersonImport) (created bool, err error)
}

// Domain is everything the handlers need. *training.Service implements it.
type Domain interface {
	Trainer
	PeopleEditor
}

var _ Domain = (*training.Service)(nil)

// Options configure the handler.
type Options struct {
	Static fs.FS        // the page and its assets, served at /
	Logger *slog.Logger // nil discards logs
}

// New returns the HTTP handler for the page, the API, /healthz and /openapi.yaml.
func New(svc Domain, opts Options) (http.Handler, error) {
	if svc == nil || opts.Static == nil {
		return nil, errors.New("httpapi: a Domain and Static files are required")
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	spec, err := loadSpec()
	if err != nil {
		return nil, err
	}
	h := &handlers{svc: svc, log: log}

	validate := middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
		DoNotValidateServers: true, // the service runs behind a gateway under any host name
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, o middleware.ErrorHandlerOpts) {
			h.writeError(w, o.StatusCode, err.Error())
		},
	})
	strict := oapi.NewStrictHandlerWithOptions(h, nil, oapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			h.writeError(w, http.StatusBadRequest, err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error("write response", "path", r.URL.Path, "err", err)
			h.writeError(w, http.StatusInternalServerError, "internal error")
		},
	})
	api := http.NewServeMux()
	oapi.HandlerFromMux(strict, api)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHeaders(validate(api)))
	mux.Handle("/healthz", api)
	mux.HandleFunc("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(apispec.YAML)
	})
	mux.Handle("/", static(opts.Static))
	return logRequests(log, mux), nil
}

// loadSpec parses and checks the embedded OpenAPI description.
func loadSpec() (*openapi3.T, error) {
	spec, err := openapi3.NewLoader().LoadFromData(apispec.YAML)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec: %w", err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("invalid OpenAPI spec: %w", err)
	}
	return spec, nil
}

func apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

func static(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The page is small and changes with each release; always revalidate.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		fileServer.ServeHTTP(w, r)
	})
}

// handlers implements oapi.StrictServerInterface on top of the Domain.
type handlers struct {
	svc Domain
	log *slog.Logger
}

var _ oapi.StrictServerInterface = (*handlers)(nil)

func (h *handlers) GetHealth(context.Context, oapi.GetHealthRequestObject) (oapi.GetHealthResponseObject, error) {
	return oapi.GetHealth200TextResponse("ok\n"), nil
}

func (h *handlers) ListPeople(ctx context.Context, _ oapi.ListPeopleRequestObject) (oapi.ListPeopleResponseObject, error) {
	people, err := h.svc.People(ctx)
	if err != nil {
		h.log.Error("list people", "err", err)
		return oapi.ListPeople500JSONResponse{InternalErrorJSONResponse: internalError}, nil
	}
	out := make(oapi.ListPeople200JSONResponse, len(people))
	for i, p := range people {
		out[i] = toPerson(p)
	}
	return out, nil
}

func (h *handlers) PutPerson(ctx context.Context, req oapi.PutPersonRequestObject) (oapi.PutPersonResponseObject, error) {
	in := fromPersonInput(req.Key, *req.Body)
	created, err := h.svc.SavePerson(ctx, in)
	switch {
	case errors.Is(err, training.ErrInvalid):
		return oapi.PutPerson400JSONResponse{BadRequestJSONResponse: oapi.BadRequestJSONResponse{Error: err.Error()}}, nil
	case err != nil:
		h.log.Error("save person", "person", req.Key, "err", err)
		return oapi.PutPerson500JSONResponse{InternalErrorJSONResponse: internalError}, nil
	case created:
		return oapi.PutPerson201JSONResponse(toPerson(in.Person)), nil
	}
	return oapi.PutPerson200JSONResponse(toPerson(in.Person)), nil
}

func (h *handlers) GetToday(ctx context.Context, req oapi.GetTodayRequestObject) (oapi.GetTodayResponseObject, error) {
	day := h.svc.Today()
	if req.Params.Date != nil {
		day = training.DateOf(req.Params.Date.Time)
	}
	sess, err := h.svc.Session(ctx, req.Key, day)
	switch {
	case errors.Is(err, training.ErrNotFound):
		return oapi.GetToday404JSONResponse{NotFoundJSONResponse: oapi.NotFoundJSONResponse{Error: err.Error()}}, nil
	case err != nil:
		h.log.Error("session", "person", req.Key, "day", day, "err", err)
		return oapi.GetToday500JSONResponse{InternalErrorJSONResponse: internalError}, nil
	}
	return oapi.GetToday200JSONResponse(toSession(sess)), nil
}

func (h *handlers) AddRound(ctx context.Context, req oapi.AddRoundRequestObject) (oapi.AddRoundResponseObject, error) {
	st, err := h.svc.RecordRound(ctx, req.Key, fromRoundInput(*req.Body))
	switch {
	case errors.Is(err, training.ErrNotFound):
		return oapi.AddRound404JSONResponse{NotFoundJSONResponse: oapi.NotFoundJSONResponse{Error: err.Error()}}, nil
	case errors.Is(err, training.ErrInvalid):
		return oapi.AddRound400JSONResponse{BadRequestJSONResponse: oapi.BadRequestJSONResponse{Error: err.Error()}}, nil
	case err != nil:
		h.log.Error("record round", "person", req.Key, "err", err)
		return oapi.AddRound500JSONResponse{InternalErrorJSONResponse: internalError}, nil
	}
	return oapi.AddRound200JSONResponse(toStats(st)), nil
}

// internalError hides details from clients; they go to the log.
var internalError = oapi.InternalErrorJSONResponse{Error: "internal error"}

func (h *handlers) writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(oapi.Error{Error: msg}); err != nil {
		h.log.Warn("write error response", "err", err)
	}
}

// statusRecorder remembers the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/healthz" {
			log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.code, "ms", time.Since(start).Milliseconds())
		}
	})
}
