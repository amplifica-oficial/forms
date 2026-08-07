package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/markbates/goth"
	"github.com/markbates/goth/providers/google"
	_ "modernc.org/sqlite"
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
	config    Config
	keepAlive chan<- struct{}
}

func (s *Service) GetProvider() goth.Provider {
	provider := google.New(s.config.OAUTH_GOOGLE_KEY, s.config.OAUTH_GOOGLE_SECRET, s.config.FRONTEND_URL,
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
	)
	provider.SetPrompt("select_account")

	return provider
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
	AllowLogin   bool

	Open  *time.Time
	Close *time.Time
}

var reValidateSlug = regexp.MustCompile(`^[a-z\-]{3,}$`)

func (s *Service) PutForm(ctx context.Context, req Request[Nil, PutFormReq]) (*PutFormReq, error) {
	form := req.Body

	if !reValidateSlug.MatchString(form.Slug) {
		return nil, ErrCode("request/slug", "bad slug")
	}

	if form.Target != "" {
		targetURL, err := url.Parse(form.Target)
		if err != nil {
			return nil, ErrCode("request/target", "bad target")
		}
		if form.Target != targetURL.String() {
			return nil, ErrCode("request/target", "bad target")
		}

		if targetURL.Scheme == "https" {
			// ok
		} else if targetURL.Scheme == "http" && s.config.ALLOW_INSECURE_TARGET {
			// ok
		} else {
			return nil, ErrCode("request/target-scheme", "only https allowed")
		}
	}

	if form.Open != nil && form.Close != nil {
		if form.Open.After(*form.Close) {
			return nil, ErrCode("request/bad-dates", "open can't be after close")
		}
	}

	if s.config.API_TOKEN != "" {
		auth := req.R.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok {
			return nil, ErrAuth("no Bearer prefix in Authorization Header")
		}
		if token != s.config.API_TOKEN {
			return nil, ErrAuth("bad token")
		}
	}

	if form.RequireLogin {
		form.AllowLogin = true
	}

	now := RequestTimeFrom(ctx)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO form (slug, target, target_token, open, close, requires_login, allows_login, email_domain, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (slug) DO UPDATE
			SET target         = excluded.target,
				target_token   = excluded.target_token,
				open           = excluded.open,
				close          = excluded.close,
				requires_login = excluded.requires_login,
				allows_login   = excluded.allows_login,
				email_domain   = excluded.email_domain,
				updated_at     = excluded.updated_at
	`, form.Slug, form.Target, form.TargetToken, Time(form.Open), Time(form.Close), form.RequireLogin, form.AllowLogin, form.EmailDomain, Time(&now), Time(&now))
	if err != nil {
		return nil, Err(err)
	}

	return &form, nil
}

type FormInfo struct {
	LoginUrl      string
	RequiresLogin bool
}

func (s *Service) ClientGetFormInfo(ctx context.Context, req Request[struct {
	Slug string `url:"query"`
}, Nil]) (*FormInfo, error) {
	slug := req.Query.Slug

	var form FormInfo
	var formAllowsLogin bool
	var formOpen *time.Time
	var formClose *time.Time

	formRow := s.db.QueryRowContext(ctx, `SELECT open, close, requires_login, allows_login FROM form WHERE slug = ?`, slug)
	if err := formRow.Scan(&formOpen, &formClose, &form.RequiresLogin, &formAllowsLogin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound("form not found: '%s'", slug)
		}
		return nil, Err(err)
	}

	now := RequestTimeFrom(ctx)
	if formOpen != nil && now.Before(*formOpen) {
		return nil, ErrCode("request/before-open", "form '%s' opens at %v", slug, formOpen)
	}
	if formClose != nil && now.After(*formClose) {
		return nil, ErrCode("request/after-close", "form '%s' closed at %v", slug, formClose)
	}

	if formAllowsLogin {
		state := make([]byte, 16)
		rand.Read(state)

		token, err := jwt.NewWithClaims(jwt.SigningMethodHS512, &jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.config.LOGIN_JWT_DURATION)),
			Audience:  jwt.ClaimStrings{slug},
			ID:        base64.StdEncoding.EncodeToString(state),
		}).SignedString([]byte(s.config.LOGIN_JWT_SIGNING_KEY))
		if err != nil {
			return nil, Err(err)
		}

		session, err := s.GetProvider().BeginAuth(token)
		if err != nil {
			return nil, Err(err)
		}

		form.LoginUrl, err = session.GetAuthURL()
		if err != nil {
			return nil, Err(err)
		}
	}

	return &form, nil
}

type CompleteLoginParams struct {
	State string `url:"query"`
	Code  string `url:"query"`
}

// CompleteLoginParams.Get implements goth.Params interface
func (params *CompleteLoginParams) Get(key string) string {
	switch key {
	case "code":
		return params.Code
	}
	panic("CompleteLoginParams.Get() is only for getting oauth params")
}

type CompleteLoginRes struct {
	Token string
}

func (s *Service) CompleteLogin(ctx context.Context, req Request[CompleteLoginParams, Nil]) (*CompleteLoginRes, error) {
	var user goth.User
	var formSlug string

	{
		token := req.Query.State
		var claims jwt.RegisteredClaims
		jwt, err := jwt.ParseWithClaims(token, &claims, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS512 {
				return nil, ErrInvalidToken("bad signing method")
			}
			return []byte(s.config.LOGIN_JWT_SIGNING_KEY), nil
		})
		if err != nil {
			return nil, ErrAuth("bad state: %s", err)
		}
		if !jwt.Valid {
			return nil, ErrAuth("old state: %s", err)
		}

		provider := s.GetProvider()
		session, err := provider.BeginAuth(token)
		if err != nil {
			return nil, Err(err)
		}

		_, err = session.Authorize(provider, &req.Query)
		if err != nil {
			return nil, Err(err)
		}

		user, err = provider.FetchUser(session)
		if err != nil {
			return nil, Err(err)
		}

		formSlug = claims.Audience[0]
	}

	// Create actual token for response
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS512, &jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.config.RESPONSE_JWT_DURATION)),
		Audience:  jwt.ClaimStrings{formSlug},
		Subject:   user.Email,
	}).SignedString([]byte(s.config.RESPONSE_JWT_SIGNING_KEY))
	if err != nil {
		return nil, Err(err)
	}

	return &CompleteLoginRes{
		Token: token,
	}, nil
}

type CreateResponseReq struct {
	Slug  string
	Email string
	Name  string
	Token string
}

type FormResponseConfirmation struct {
	CreatedAt       *time.Time `json:",omitempty"`
	AlreadyAnswered bool       `json:",omitempty"`
}

func (s *Service) CreateResponse(ctx context.Context, req Request[Nil, CreateResponseReq]) (*FormResponseConfirmation, error) {
	data := req.Body

	data.Slug = strings.TrimSpace(data.Slug)
	data.Name = strings.TrimSpace(data.Name)
	data.Email = strings.TrimSpace(data.Email)

	if !reValidateSlug.MatchString(data.Slug) {
		return nil, ErrCode("request/missing-slug", "no form")
	}

	if data.Token != "" {
		var claims jwt.RegisteredClaims
		jwt, err := jwt.ParseWithClaims(data.Token, &claims, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS512 {
				return nil, ErrInvalidToken("bad signing method")
			}
			return []byte(s.config.RESPONSE_JWT_SIGNING_KEY), nil
		})
		if err != nil {
			return nil, ErrAuth("bad jwt: %s", err)
		}
		if !jwt.Valid {
			return nil, ErrAuth("invalid jwt: %s", err)
		}
		if claims.Audience[0] != data.Slug {
			return nil, ErrBadRequest("token for other form: '%s'", claims.Subject)
		}
		data.Email = claims.Subject
	}

	if data.Email == "" {
		return nil, ErrCode("request/missing-email", "email or token missing")
	} else {
		email, err := mail.ParseAddress(data.Email)
		if err != nil {
			return nil, ErrCode("request/bad-email", "email is not valid")
		}
		if email.Address != data.Email {
			return nil, ErrCode("request/bad-email", "email is not in simplest form")
		}
	}

	if data.Name == "" {
		return nil, ErrCode("request/missing-name", "name is missing")
	}

	var formID int
	var formOpen, formClose *time.Time
	var formEmailDomain string
	var formRequiresLogin bool

	confirmation := FormResponseConfirmation{}

	if err := RunTx(ctx, s.db, func(db *sql.Tx) error {
		formRow := db.QueryRowContext(ctx, `SELECT id, open, close, requires_login FROM form WHERE slug = ?`, data.Slug)
		if err := formRow.Scan(&formID, &formOpen, &formClose, &formRequiresLogin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound("form not found: '%s'", data.Slug)
			}
			return Err(err)
		}

		if formRequiresLogin && data.Token == "" {
			return ErrAuth("this form requires login")
		}

		now := RequestTimeFrom(ctx)
		if formOpen != nil && now.Before(*formOpen) {
			return ErrCode("request/before-open", "form '%s' opens at %v", data.Slug, formOpen)
		}
		if formClose != nil && now.After(*formClose) {
			return ErrCode("request/after-close", "form '%s' closed at %v", data.Slug, formClose)
		}
		if formEmailDomain != "" && !strings.HasSuffix(data.Email, "@"+formEmailDomain) {
			return ErrCode("request/bad-domain", "email '%s' does not belong to the domain '%s'", data.Email, formEmailDomain)
		}

		_, err := db.ExecContext(ctx, `INSERT INTO response (form_id, email, name, created_at) VALUES (?, ?, ?, ?)`, formID, data.Email, data.Name, Time(&now))
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				confirmation.AlreadyAnswered = true
				return nil
			}
			return Err(err)
		}

		confirmation.CreatedAt = &now
		return nil
	}); err != nil {
		return nil, err
	}

	return &confirmation, nil
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

	applyiedVersion := 0

	row := db.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM migration`)
	if err = row.Scan(&applyiedVersion); err != nil {
		return Err(err)
	}
	slog.Debug("starting", "applyiedVersion", applyiedVersion)

	thisVersion := 1
	if applyiedVersion < thisVersion {
		slog.Debug("run-migration", "applyiedVersion", applyiedVersion, "thisVersion", thisVersion)
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

			_, err = db.ExecContext(ctx, `INSERT INTO migration (version, migrated_at) VALUES (?, ?)`, thisVersion, Now())
			if err != nil {
				return Err(err)
			}

			return nil
		})
	}

	thisVersion = 2
	if applyiedVersion < thisVersion {
		slog.Debug("run-migration", "applyiedVersion", applyiedVersion, "thisVersion", thisVersion)

		err = RunTx(ctx, db, func(db *sql.Tx) error {
			_, err := db.ExecContext(ctx, `ALTER TABLE form ADD allows_login BOOLEAN NOT NULL DEFAULT 0`)
			if err != nil {
				return Err(err)
			}

			_, err = db.ExecContext(ctx, `UPDATE form SET allows_login = 1 WHERE requires_login = 1`)
			if err != nil {
				return Err(err)
			}

			_, err = db.ExecContext(ctx, `INSERT INTO migration (version, migrated_at) VALUES (?, ?)`, thisVersion, Now())
			if err != nil {
				return Err(err)
			}

			return nil
		})
	}

	return err
}

type Config struct {
	API_ADDR     string `default:":5000"`
	FRONTEND_URL string `default:"http://localhost:3000"`

	// Required
	API_TOKEN                  string
	RESPONSE_JWT_SIGNING_KEY   string
	LOGIN_JWT_SIGNING_KEY      string
	VALIDATION_JWT_SIGNING_KEY string

	RESPONSE_JWT_DURATION time.Duration `default:"30m"`
	LOGIN_JWT_DURATION    time.Duration `default:"30m"`

	OAUTH_GOOGLE_KEY    string `default:""`
	OAUTH_GOOGLE_SECRET string `default:""`

	SYNC_BATCH_SIZE        int  `default:"100"`
	RECOVER_SYNC_RESPONSES bool `default:"true"`

	INACTIVE_TIMEOUT      time.Duration `default:"720h"`
	ALLOW_INSECURE_TARGET bool          `default:"false"`
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
		case int, int32, int64:
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				panic(fmt.Sprintf("invalid int '%s' for env %s", value, fieldType.Name))
			}
			fieldValue.SetInt(v)
		case bool:
			v, err := strconv.ParseBool(value)
			if err != nil {
				panic(fmt.Sprintf("invalid bool '%s' for env %s", value, fieldType.Name))
			}
			fieldValue.SetBool(v)
		default:
			panic("unknown type")
		}
	}

	return config
}

type SyncResponseData struct {
	formID      int
	target      string
	targetToken string

	Form      string                   `json:"form"`
	Responses []SyncSingleResponseData `json:"responses"`
}
type SyncSingleResponseData struct {
	id        int
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Service) ApplySync(data SyncResponseData) error {
	url, err := url.Parse(data.target)
	if err != nil {
		return Err(err)
	}

	var buffer bytes.Buffer
	err = json.NewEncoder(&buffer).Encode(data)
	if err != nil {
		return Err(err)
	}

	req := http.Request{
		Method: http.MethodPost,
		Body:   io.NopCloser(&buffer),
		URL:    url,
		Header: make(http.Header),
	}
	req.Header.Add("Content-Type", "application/json")

	if data.targetToken != "" {
		req.Header.Add("Authorization", "Bearer "+data.targetToken)
	}

	res, err := s.client.Do(&req)
	if err != nil {
		return Err(err)
	}
	res.Body.Close()
	if res.StatusCode < 200 || 299 < res.StatusCode {
		// Not good
		return ErrCode("sync/target-fail", "target '%s' failed with status '%v'", data.target, res.StatusCode)
	}

	var setSyncedQuery strings.Builder
	setSyncedQuery.WriteString(`UPDATE response SET synced = 1 WHERE id in (`)
	for i, r := range data.Responses {
		if i != 0 {
			setSyncedQuery.WriteString(",")
		}
		setSyncedQuery.WriteString(strconv.Itoa(r.id))
	}
	setSyncedQuery.WriteString(`)`)
	_, err = s.db.Exec(setSyncedQuery.String())
	return Err(err)
}

func (s *Service) doSyncResponses() error {
	data := SyncResponseData{
		Responses: make([]SyncSingleResponseData, 0, 4096),
	}

	rowsData := make([]struct {
		FormID      int
		Form        string
		Target      string
		TargetToken string
		ResponseID  int
		Email       string
		Name        string
		CreatedAt   time.Time
	}, 4096)

	for {
		slog.Debug("sync-start")
		var syncedAnything = false

		rows, err := s.db.Query(`
			SELECT f.id, f.slug, f.target, f.target_token, r.id, r.email, r.name, r.created_at
			FROM response r
			LEFT JOIN form f ON f.id = r.form_id
			WHERE r.synced = 0 AND f.target != ''
			ORDER BY r.form_id`)
		if err != nil {
			return Err(err)
		}
		defer rows.Close()

		rowCount := 0
		formCount := 0
		currentForm := 0
		for rows.Next() {
			if rowCount == 0 {
				s.keepAlive <- struct{}{}
			}

			r := &rowsData[rowCount]
			err := rows.Scan(&r.FormID, &r.Form, &r.Target, &r.TargetToken, &r.ResponseID, &r.Email, &r.Name, &r.CreatedAt)
			if err != nil {
				return Err(err)
			}

			if r.FormID != currentForm && currentForm != 0 {
				formCount++
				currentForm = r.FormID
			}
			rowCount++
		}
		if err := rows.Err(); err != nil {
			return Err(err)
		}
		rows.Close()

		slog.Debug("sync-found", "responses", rowCount, "forms", formCount)

		for i := range rowCount {
			r := &rowsData[i]
			if (r.FormID != data.formID || len(data.Responses) == s.config.SYNC_BATCH_SIZE) && data.formID != 0 {
				err := s.ApplySync(data)
				if err != nil {
					slog.Error("sync-batch-fail", "error", err.Error())
				} else {
					syncedAnything = true
				}
				data.Responses = data.Responses[:0]
			}

			data.formID = r.FormID
			data.Form = r.Form
			data.target = r.Target
			data.targetToken = r.TargetToken
			data.Responses = append(data.Responses, SyncSingleResponseData{
				id:        r.ResponseID,
				Email:     r.Email,
				Name:      r.Name,
				CreatedAt: r.CreatedAt,
			})
		}

		if data.target != "" && len(data.Responses) != 0 {
			err := s.ApplySync(data)
			if err != nil {
				slog.Error("sync-batch-fail", "error", err.Error())
			} else {
				syncedAnything = true
			}
			data.Responses = data.Responses[:0]
		}

		if !syncedAnything {
			time.Sleep(5 * time.Second)
		}
	}
}

func (s *Service) recoverSyncResponses() (err error) {
	if s.config.RECOVER_SYNC_RESPONSES {
		defer func() {
			if rec := recover(); rec != nil {
				var ok bool
				err, ok = rec.(error)
				if ok {
					return
				}
				err = fmt.Errorf("recovered: %v", rec)
			}
		}()
	}

	err = s.doSyncResponses()
	return
}

func (s *Service) SyncResponses() {
	err := s.recoverSyncResponses()
	for err != nil {
		slog.Error("sync-fail", "error", err)
		err = s.recoverSyncResponses()
	}
}

func (s *Service) EndProcessAfterInactiveTimeAndNoUnsyncedResponse(keepAlive <-chan struct{}, maxInactiveTime time.Duration) {
	for {
		select {
		case <-time.After(maxInactiveTime):
			// Inactive, exit but only if no unsynced responses
			RunTx(context.Background(), s.db, func(db *sql.Tx) error {
				row := db.QueryRow(`SELECT r.id FROM response r LEFT JOIN form f ON f.id = r.form_id WHERE r.synced = 0 AND f.target != '' LIMIT 1`)
				var id int
				err := row.Scan(&id)
				if errors.Is(err, sql.ErrNoRows) {
					slog.Info("inactive-shutdown")
					os.Exit(0)
				}
				return nil
			})
		case <-keepAlive:
			// ok
		}
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	db, err := sql.Open("sqlite", "forms.db?cache=shared&mode=rwc&_fk=1&_journal=WAL&_writable_schema=0")
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

	keepAlive := make(chan struct{}, 128)
	s := Service{
		db: db,
		client: http.Client{
			Timeout: 10 * time.Second,
		},
		config:    MustLoadConfig(),
		keepAlive: keepAlive,
	}

	mux := http.NewServeMux()
	m := NewMiddlewareGroup(
		AddRequestTime,
		LimitBody(2*1024),
		LogRequests,
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.keepAlive <- struct{}{}

				origin := r.Header.Get("Origin")
				if origin == s.config.FRONTEND_URL {
					h := w.Header()
					h.Add("Access-Control-Allow-Origin", s.config.FRONTEND_URL)
					h.Add("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
					h.Add("Access-Control-Allow-Headers", "Content-Type")
					h.Add("Access-Control-Max-Age", "86400")
				}
				next.ServeHTTP(w, r)
			})
		},
	)

	mux.Handle("OPTIONS /", m.ApplyFn(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	mux.Handle("GET /", m.ApplyFn(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	}))

	mux.Handle("PUT /PutForm", m.ApplyFn(HandleRequestReturner(s.PutForm)))
	mux.Handle("POST /CreateResponse", m.ApplyFn(HandleRequestReturner(s.CreateResponse)))
	mux.Handle("GET /ClientGetFormInfo", m.ApplyFn(HandleRequestReturner(s.ClientGetFormInfo)))
	mux.Handle("POST /CompleteLogin", m.ApplyFn(HandleRequestReturner(s.CompleteLogin)))

	server := http.Server{
		Addr: s.config.API_ADDR,

		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,

		Handler: mux,
	}

	go s.SyncResponses()
	go s.EndProcessAfterInactiveTimeAndNoUnsyncedResponse(keepAlive, s.config.INACTIVE_TIMEOUT)

	slog.Info("start-server", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "server shutdown: %v", err)
	}
}
