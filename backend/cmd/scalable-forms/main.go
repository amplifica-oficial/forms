package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
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

type Service struct {
	client    http.Client
	db        *sql.DB
}

func Time(t *time.Time) *string {
	if (t == nil) {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

type PutFormReq struct {
	Slug   string
	Target string
	TargetToken string

	Open  *time.Time
	Close *time.Time
}

var reValidateSlug = regexp.MustCompile(`^[a-z\-]{3,}$`)

func (r PutFormReq) Validate() string {
	if !reValidateSlug.MatchString(r.Slug) {
		return "slug"
	}

	if r.Target == ""{
		return "target"
	}
	targetURL, err := url.Parse(r.Target)
	if err != nil {
		return "target"
	}
	if r.Target != targetURL.String() {
		return "target"
	}
	if targetURL.Scheme != "https" && targetURL.Scheme != "http" {
		return "target"
	}

	if r.Open != nil && r.Close != nil {
		if r.Open.After(*r.Close) {
			return "time"
		}
	}

	return ""
}

func (s *Service) PutForm(ctx context.Context, req Request[Nil, PutFormReq]) (*PutFormReq, error) {
	form := req.Body

	now := RequestTimeFrom(ctx)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO form (slug, target, target_token, open, close, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (slug) DO UPDATE
			SET target       = excluded.target,
				target_token = excluded.target_token,
				open         = excluded.open,
				close        = excluded.close,
				updated_at   = excluded.updated_at
	`, form.Slug, form.Target, form.TargetToken, Time(form.Open), Time(form.Close), Time(&now), Time(&now))
	if err != nil {
		return nil, Err(err)
	}

	return &form, nil
}

type CreateResponseReq struct {
	Slug  string
	Email string
	Name  string
}

func (r CreateResponseReq) Validate() string {
	if !reValidateSlug.MatchString(r.Slug) {
		return "slug"
	}

	email, err := mail.ParseAddress(r.Email)
	if err != nil {
		return "e-mail"
	}
	if email.Address != r.Email {
		return "e-mail"
	}

	if r.Name != strings.TrimSpace(r.Name) || r.Name == "" {
		return "name"
	}

	return ""
}

type FormResponseToken struct {
	Slug      string
	Email     string
	Name      string
	CreatedAt time.Time
	Signature string
}

func (s *Service) CreateResponse(ctx context.Context, req Request[Nil, CreateResponseReq]) (*FormResponseToken, error) {
	resp := req.Body

	resp.Slug = strings.TrimSpace(resp.Slug)
	resp.Name = strings.TrimSpace(resp.Name)
	resp.Email = strings.TrimSpace(resp.Email)

	if resp.Name == "" || resp.Email == "" {
		return nil, ErrBadRequest("name or email missing")
	}

	var formID int
	var formOpen, formClose *time.Time

	if err := RunTx(ctx, s.db, func(db *sql.Tx) error {

		formRow := db.QueryRowContext(ctx, `SELECT id, open, close FROM form WHERE slug = ?`, resp.Slug)
		if err := formRow.Scan(&formID, &formOpen, &formClose); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound("form not found: '%s'", resp.Slug)
			}
			return Err(err)
		}

		now := RequestTimeFrom(ctx)
		_, err := db.ExecContext(ctx, `INSERT INTO response (form_id, email, name, created_at) VALUES (?, ?, ?, ?)`, formID, resp.Email, resp.Name, Time(&now))
		if err != nil {
			return Err(err)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	token := FormResponseToken{
		Slug:      resp.Slug,
		Email:     resp.Email, // TODO: validate
		Name:      resp.Name,
		CreatedAt: RequestTimeFrom(ctx),
		Signature: "i-say-its-ok",
	}

	return &token, nil
}

func RunTx(ctx context.Context, db *sql.DB, fn func(db *sql.Tx) error) error {
	var err error
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelLinearizable})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	err = fn(tx)
	if err != nil {
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	return nil
}

func RunMigrations(ctx context.Context, db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS migration (
		version INTEGER PRIMARY KEY,
		migrated_at DATETIME
	)`)
	if err != nil {
		return Err(err)
	}

	version := 0

	row := db.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM migration`)
	if err = row.Scan(&version); err != nil {
		return Err(err)
	}

	if err == nil && version < 1 {
		slog.Debug("db-migration-1")
		err = RunTx(ctx, db, func(db *sql.Tx) error {
			_, err := db.ExecContext(ctx, `CREATE TABLE form (
				id   INTEGER PRIMARY KEY,
				slug TEXT NOT NULL UNIQUE,

				target       TEXT NOT NULL,
				target_token TEXT,

				open  DATETIME,
				close DATETIME,

				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`)
			if err != nil {
				return Err(err)
			}

			_, err = db.ExecContext(ctx, `CREATE TABLE response (
				id INTEGER PRIMARY KEY,

				form_id INTEGER NOT NULL,
				email   TEXT NOT NULL,
				name    TEXT NOT NULL,
				synced  BOOLEAN NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,

				UNIQUE(form_id, email)
			)`)
			if err != nil {
				return Err(err)
			}

			_, err = db.ExecContext(ctx, `INSERT INTO migration (version, migrated_at) VALUES (?, ?)`, 1, Now())
			if err != nil {
				return Err(err)
			}

			return nil
		})
	}

	return err
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	db, err := sql.Open("sqlite3", "forms.db?cache=private&_fk=1&_journal=WAL&_writable_schema=0")
	if err != nil {
		slog.Error("db-open", "error", err)
		os.Exit(-1)
	}

	slog.Debug("start-migrations")
	ctx := context.Background()
	if err := RunMigrations(ctx, db); err != nil {
		slog.Error("db-migrations", "error", err)
		os.Exit(-1)
	}

	mux := http.NewServeMux()
	m := NewMiddlewareGroup(
		AddRequestTime,
		LimitBody(2*1024*1024),
		LogRequests,
	)
	s := Service{
		db: db,
		client: http.Client{
			Timeout: 10 * time.Second,
		},
	}

	mux.Handle("GET /", m.ApplyFn(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	}))

	mux.Handle("PUT /PutForm", m.ApplyFn(HandleRequestReturner(s.PutForm)))
	mux.Handle("POST /CreateResponse", m.ApplyFn(HandleRequestReturner(s.CreateResponse)))

	server := http.Server{
		Addr: ":5000",

		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,

		Handler: mux,
	}

	slog.Info("start-server", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "server shutdown: %v", err)
	}
}
