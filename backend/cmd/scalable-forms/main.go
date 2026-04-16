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
	"reflect"
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
	client http.Client
	db     *sql.DB
	config Config
}

func Time(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

type PutFormReq struct {
	Slug        string
	Target      string
	TargetToken string

	EmailDomain  string
	RequireLogin bool

	Open  *time.Time
	Close *time.Time
}

var reValidateSlug = regexp.MustCompile(`^[a-z\-]{3,}$`)

func (r PutFormReq) Validate() string {
	if !reValidateSlug.MatchString(r.Slug) {
		return "slug"
	}

	if r.Target == "" {
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
		INSERT INTO form (slug, target, target_token, open, close, requires_login, email_domain, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (slug) DO UPDATE
			SET target         = excluded.target,
				target_token   = excluded.target_token,
				open           = excluded.open,
				close          = excluded.close,
				requires_login = excluded.requires_login,
				email_domain   = excluded.email_domain,
				updated_at     = excluded.updated_at
	`, form.Slug, form.Target, form.TargetToken, Time(form.Open), Time(form.Close), form.RequireLogin, form.EmailDomain, Time(&now), Time(&now))
	if err != nil {
		return nil, Err(err)
	}

	return &form, nil
}

type FormInfo struct {
	Open          *time.Time
	Close         *time.Time
	RequiresLogin bool
}

func (s *Service) ClientGetFormInfo(ctx context.Context, req Request[struct {
	Slug string `url:"param"`
}, Nil]) (*FormInfo, error) {
	slug := req.Query.Slug

	var form FormInfo
	formRow := s.db.QueryRowContext(ctx, `SELECT open, close, requires_login FROM form WHERE slug = ?`, slug)
	if err := formRow.Scan(&form.Open, &form.Close, &form.RequiresLogin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound("form not found: '%s'", slug)
		}
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
	data := req.Body

	data.Slug = strings.TrimSpace(data.Slug)
	data.Name = strings.TrimSpace(data.Name)
	data.Email = strings.TrimSpace(data.Email)

	if data.Name == "" || data.Email == "" {
		return nil, ErrBadRequest("name or email missing")
	}

	var formID int
	var formOpen, formClose *time.Time
	var formEmailDomain string
	var formRequiresLogin bool

	if err := RunTx(ctx, s.db, func(db *sql.Tx) error {
		formRow := db.QueryRowContext(ctx, `SELECT id, open, close, requires_login FROM form WHERE slug = ?`, data.Slug)
		if err := formRow.Scan(&formID, &formOpen, &formClose, &formRequiresLogin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound("form not found: '%s'", data.Slug)
			}
			return Err(err)
		}

		now := RequestTimeFrom(ctx)
		if formOpen != nil && formOpen.Before(now) {
			return ErrNotFound("form '%s' opens at %v", data.Slug, formOpen)
		}
		if formClose != nil && formClose.After(now) {
			return ErrNotFound("form '%s' closed at %v", data.Slug, formClose)
		}
		if formEmailDomain != "" && !strings.HasSuffix(data.Email, "@"+formEmailDomain) {
			return ErrBadRequest("this email does not belong to the expected")
		}

		_, err := db.ExecContext(ctx, `INSERT INTO response (form_id, email, name, created_at) VALUES (?, ?, ?, ?)`, formID, data.Email, data.Name, Time(&now))
		if err != nil {
			return Err(err)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	token := FormResponseToken{
		Slug:      data.Slug,
		Email:     data.Email,
		Name:      data.Name,
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

	if version < 1 {
		slog.Debug("db-migration-1")
		err = RunTx(ctx, db, func(db *sql.Tx) error {
			_, err := db.ExecContext(ctx, `CREATE TABLE form (
				id   INTEGER PRIMARY KEY,
				slug TEXT NOT NULL UNIQUE,

				target       TEXT NOT NULL,
				target_token TEXT NOT NULL,

				open  DATETIME,
				close DATETIME,
				requires_login BOOLEAN NOT NULL,
				email_domain TEXT NOT NULL,

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

type Config struct {
	API_URL string `default:""`

	JWT_DURATION    time.Duration `default:"5m"`
	JWT_SIGNING_KEY string        `default:""`

	MAX_LOGIN_DURATION time.Duration `default:"5m"`

	OAUTH_GOOGLE_KEY    string `default:""`
	OAUTH_GOOGLE_SECRET string `default:""`
}

func MustLoadConfig() Config {
	config := Config{}

	configValue := reflect.ValueOf(&config).Elem()
	configType := configValue.Type()
	for i := 0; i < configType.NumField(); i++ {
		fieldType := configType.Field(i)
		fieldValue := configValue.Field(i)

		value, ok := os.LookupEnv(fieldType.Name)
		if !ok {
			value, ok = fieldType.Tag.Lookup("default")
			if !ok {
				panic(fmt.Sprintf("no env value for config %s, which has no default", fieldType.Name))
			}
		}

		field := fieldValue.Interface()
		switch field.(type) {
		case string:
			fieldValue.SetString(value)
		case time.Duration:
			duration, err := time.ParseDuration(value)
			if err != nil {
				panic(fmt.Sprintf("invalid duration '%s' for env %s", value, fieldType.Name))
			}
			fieldValue.SetInt(int64(duration))
		}
	}

	return config
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
		LimitBody(2*1024),
		LogRequests,
	)
	s := Service{
		db: db,
		client: http.Client{
			Timeout: 10 * time.Second,
		},
		config: MustLoadConfig(),
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
