package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Middleware = func(http.Handler) http.Handler

type MiddlewareGroup struct {
	Middlewares []Middleware
}

func NewMiddlewareGroup(middlewares ...Middleware) MiddlewareGroup {
	var m MiddlewareGroup
	for _, ware := range middlewares {
		m.Middlewares = append(m.Middlewares, ware)
	}
	return m
}

func (m *MiddlewareGroup) Push(middlewares ...Middleware) {
	for _, ware := range middlewares {
		m.Middlewares = append(m.Middlewares, ware)
	}
}

func (m *MiddlewareGroup) With(middlewares ...Middleware) MiddlewareGroup {
	var newM MiddlewareGroup
	newM.Middlewares = append(newM.Middlewares, m.Middlewares...)
	for _, ware := range middlewares {
		newM.Middlewares = append(newM.Middlewares, ware)
	}
	return newM
}

func (m *MiddlewareGroup) Apply(next http.Handler) http.Handler {
	for i := len(m.Middlewares) - 1; i >= 0; i-- {
		this := m.Middlewares[i]
		next = this(next)
	}
	return next
}

func (m *MiddlewareGroup) ApplyFn(nextFunc http.HandlerFunc) http.Handler {
	return m.Apply(nextFunc)
}

func LimitBody(bytes int64) func(http.Handler) http.Handler {
	type ReadCloser struct {
		io.Reader
		io.Closer
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = &ReadCloser{
				Reader: &io.LimitedReader{
					R: r.Body,
					N: bytes,
				},
				Closer: r.Body,
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ResponseWriterWrapper is a wrapper around http.ResponseWriter
// that records the status code and how many bytes were written.
type ResponseWriterWrapper struct {
	http.ResponseWriter

	status           int
	hasStatusBeenSet bool

	bytesWritten int
}

// Write calls the underlying ResponseWriter's Write method and records the number of bytes written.
func (w *ResponseWriterWrapper) Write(bytes []byte) (int, error) {
	w.bytesWritten += len(bytes)
	return w.ResponseWriter.Write(bytes)
}

// WriteHeader calls the underlying ResponseWriter's WriteHeader method and records the status code.
func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	if !w.hasStatusBeenSet {
		w.status = statusCode
		w.hasStatusBeenSet = true

		if statusCode != 200 {
			w.ResponseWriter.WriteHeader(statusCode)
		}
	}
}

// Unwrap returns the underlying http.ResponseWriter.
func (w *ResponseWriterWrapper) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// wrapResponseWriter wraps an http.ResponseWriter with a ResponseWriterWrapper.
func wrapResponseWriter(w http.ResponseWriter) *ResponseWriterWrapper {
	return &ResponseWriterWrapper{
		ResponseWriter: w,
		status:         200,
	}
}

// LogRequests is a middleware that logs the start and end of requests.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		before := RequestTimeFrom(ctx)

		slog.Debug("request-start",
			"target", r.URL,
			"method", r.Method,
		)

		wrapper := wrapResponseWriter(w)

		defer func() {
			requestDuration := time.Since(before)
			slog.Debug("request-complete",
				"target", r.URL,
				"method", r.Method,
				"status", wrapper.status,
				"bytes", wrapper.bytesWritten,
				"duration", requestDuration,
			)
		}()
		next.ServeHTTP(wrapper, r)
	})
}

func AddRequestTime(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx = WithRequestTime(ctx, time.Now())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type ContextKey int

const (
	keyRequestTime ContextKey = iota + 1
)

func RequestTimeFrom(ctx context.Context) time.Time {
	v, ok := ctx.Value(keyRequestTime).(time.Time)
	if !ok {
		return time.Now()
	}
	return v
}

func WithRequestTime(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, keyRequestTime, t)
}

type FormResponse struct {
	Form uuid.UUID

	Email string
	Name  string

	Synced bool

	CreatedAt time.Time
}

type FormConfiguration struct {
	ID     uuid.UUID
	Slug   string
	Target string

	Open  *time.Time
	Close *time.Time

	MaxAge               *time.Duration
	StaleWhileRevalidate *time.Duration

	CreatedAt time.Time
	UpdatedAt time.Time
}

type Service struct {
	configs   map[string]FormConfiguration
	responses []FormResponse
	client    http.Client
}

func (s *Service) PutFormConfiguration(ctx context.Context, req Request[Nil, FormConfiguration]) (*FormConfiguration, error) {
	config := req.Body

	config.Slug = strings.TrimSpace(config.Slug)
	if config.Slug == "" {
		return nil, ErrBadRequest("bad slug")
	}

	config.Target = strings.TrimSpace(config.Target)
	_, err := url.Parse(config.Target)
	if err != nil {
		return nil, ErrBadRequest("bad target url: %v", err)
	}

	if form, ok := s.configs[config.Slug]; ok {
		if form.ID != config.ID && config.ID != uuid.Nil {
			return nil, ErrConflict("form already exists with another ID")
		}

		config.ID = form.ID
		config.CreatedAt = form.CreatedAt
		config.UpdatedAt = RequestTimeFrom(ctx)
		s.configs[config.Slug] = config
	} else {
		config.ID = uuid.New()
		config.CreatedAt = RequestTimeFrom(ctx)
		config.UpdatedAt = RequestTimeFrom(ctx)
		s.configs[config.Slug] = config
	}

	return &config, nil
}

type FormResponseRequest struct {
	Slug string
	Email string
	Name  string
}

type FormResponseToken struct {
	Form uuid.UUID
	Slug string
	Email string
	Name  string
	CreatedAt time.Time
	Signature string
}

func (s *Service) CreateFormResponse(ctx context.Context, req Request[Nil, FormResponseRequest]) (*FormResponseToken, error) {
	resp := req.Body

	resp.Slug = strings.TrimSpace(resp.Slug)
	resp.Name = strings.TrimSpace(resp.Name)
	resp.Email = strings.TrimSpace(resp.Email)

	if resp.Name == "" || resp.Email == "" {
		return nil, ErrBadRequest("name or email missing")
	}

	form, ok := s.configs[resp.Slug]
	if !ok {
		return nil, ErrNotFound("this form does not exist: %s", resp.Slug)
	}


	response := FormResponse{
		Form: form.ID,
		Email: resp.Email, // TODO: validate
		Name: resp.Name,
		CreatedAt: RequestTimeFrom(ctx),
	}

	s.responses = append(s.responses, response)

	token := FormResponseToken{
		Form: form.ID,
		Slug: form.Slug,
		Email: resp.Email, // TODO: validate
		Name: resp.Name,
		CreatedAt: RequestTimeFrom(ctx),
		Signature: "i-say-its-ok",
	}

	return &token, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	mux := http.NewServeMux()
	m := NewMiddlewareGroup(
		AddRequestTime,
		LimitBody(2*1024*1024),
		LogRequests,
	)
	s := Service{
		client: http.Client{
			Timeout: 10 * time.Second,
		},
		configs: map[string]FormConfiguration{},
	}

	mux.Handle("GET /", m.ApplyFn(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	}))

	mux.Handle("PUT /PutFormConfiguration", m.ApplyFn(HandleRequestReturner(s.PutFormConfiguration)))
	mux.Handle("POST /CreateFormResponse", m.ApplyFn(HandleRequestReturner(s.CreateFormResponse)))

	server := http.Server{
		Addr: ":5000",

		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,

		Handler: mux,
	}

	slog.Info("start-server", "addr", server.Addr)
	err := server.ListenAndServe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "server shutdown: %v", err)
	}
}
