package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// HandlerFuncError is a function that handles a request and returns an error.
type HandlerFuncError func(w http.ResponseWriter, r *http.Request) error

// HandleError is a helper function that wraps a HandlerFuncError with error handling.
func HandleError(f HandlerFuncError) http.HandlerFunc {
	return f.ServeHTTP
}

// ServeHTTP implements the http.Handler interface while handling errors.
func (handler HandlerFuncError) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := handler(w, r)
	if err != nil {
		var traceLiner ErrTraceLiner

		// Log the error with the line it occurred on
		log := []any{
			"error", err,
		}
		if errors.As(err, &traceLiner) {
			log = append(log, "from", traceLiner.TraceLine())
		}
		slog.Error("request-error", log...)

		errCode, statusCode := getCodes(err)
		w.WriteHeader(statusCode)
		fmt.Fprintf(w, "{\"error\": \"%s\"}\n", errCode)
	}
}

// ErrorStatus is a list of errors and their corresponding status codes.
var ErrorStatus = []struct {
	Error             error
	StatusCode        int
	OverrideErrorCode string
}{
	{ErrAuthNotFound(), http.StatusNotFound, "not-found"},
	{ErrAuth(), http.StatusUnauthorized, ""},
	{ErrBadRequest(), http.StatusBadRequest, ""},
	{ErrNotFound(), http.StatusNotFound, ""},
	{ErrConflict(), http.StatusConflict, ""},
	{ErrForbidden(), http.StatusForbidden, ""},
}

// getCodes is a helper function that returns the error code and status code for an error.
func getCodes(err error) (string, int) {
	for _, es := range ErrorStatus {
		if errors.Is(err, es.Error) {
			if es.OverrideErrorCode != "" {
				return es.OverrideErrorCode, es.StatusCode
			}
			return err.(*Error).err, es.StatusCode
		}
	}
	return "internal", http.StatusInternalServerError
}

// ErrAuth occurs when the user is not authorized to perform an action
//
// code: auth
func ErrAuth(fmt ...any) *Error { return newError("auth", fmt...) }

// ErrAuthNotFound occurs when the user is not authorized but the
// handler layer should report it as not found
//
// code: auth/not-found
func ErrAuthNotFound(fmt ...any) *Error { return newError("auth/not-found", fmt...) }

// ErrInvalidToken occurs when the token fails to be parsed or is invalid
//
// code: auth/invalid-token
func ErrInvalidToken(fmt ...any) *Error { return newError("auth/token", fmt...) }

// ErrOAuth occurs when the OAuth flow fails
//
// code: auth/oauth
func ErrOAuth(fmt ...any) *Error { return newError("auth/oauth", fmt...) }

// ErrAuthBadRequest occurs when authentification fails because of a bad request
//
// code: auth/bad-request
func ErrAuthBadRequest(fmt ...any) *Error { return newError("auth/bad-request", fmt...) }

// ErrForbidden occurs when a user tries accessing something that he does not have access to
//
// code: forbidden
func ErrForbidden(fmt ...any) *Error { return newError("forbidden", fmt...) }

// ErrBadRequest occurs when the request is invalid
//
// code: request
func ErrBadRequest(fmt ...any) *Error { return newError("request", fmt...) }

// ErrInvalidCallback occurs when the login callback is invalid
//
// code: request/callback
func ErrInvalidCallback(fmt ...any) *Error { return newError("request/callback", fmt...) }

// ErrInvalidType occurs when the request has an invalid type
//
// code: request/type
func ErrInvalidType(fmt ...any) *Error { return newError("request/type", fmt...) }

// ErrInternal occurs when an error occurs that is not the fault of the user
//
// code: internal
func ErrInternal(fmt ...any) *Error { return newError("internal", fmt...) }

// ErrSignJWT occurs when the JWT cannot be signed
//
// code: internal/sign-jwt
func ErrSignJWT(fmt ...any) *Error { return newError("internal/sign-jwt", fmt...) }

// ErrBadConfig occurs when the configuration is invalid
//
// code: internal/bad-config
func ErrBadConfig(fmt ...any) *Error { return newError("internal/config", fmt...) }

// ErrSendResponse occurs when writing the response fails
//
// code: internal/send-response
func ErrSendResponse(fmt ...any) *Error { return newError("internal/send-response", fmt...) }

// ErrDatabase occurs when a database error occurs
//
// code: internal/database
func ErrDatabase(fmt ...any) *Error { return newError("internal/database", fmt...) }

// ErrNotFound occurs when the requested resource is not found
//
// code: not-found
func ErrNotFound(fmt ...any) *Error { return newError("not-found", fmt...) }

// ErrConflict occurs when the patch conflicts with the current state of the resource
//
// code: conflict
func ErrConflict(fmt ...any) *Error { return newError("conflict", fmt...) }

// ErrImpossible occurs when an theoretically impossible situation occurs.
// such as an entity not having a mandatory field
//
// code: impossible
func ErrImpossible(fmt ...any) *Error { return newError("impossible", fmt...) }

// When an specific condition must be communicated to the client
func ErrCode(code string, fmt ...any) *Error { return newError(code, fmt...) }

// Error is the error type used by the API.
// It is a wrapper around the standard error type that can trace the line where it was created
// and can be used to check if an error is of a specific type or subtype.
// See the Err* functions for examples.
type Error struct {
	error
	err       string
	traceLine string
}

var _ error = &Error{}
var _ ErrTraceLiner = &Error{}

// ErrTraceLiner is an interface that can be implemented by errors to return the line of code that caused the error
type ErrTraceLiner interface {
	TraceLine() string
}

var basedir string

func init() {
	_, basedir, _, _ = runtime.Caller(0)
	basedir = path.Dir(path.Dir(basedir))
}

func TraceLine(skip int) string {
	pc, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return "unable to retrieve caller"
	}
	funcName := "unknown"
	fn := runtime.FuncForPC(pc)
	if fn != nil {
		funcName = path.Base(fn.Name())
	}
	file, _ = filepath.Rel(basedir, file)
	return fmt.Sprintf("%s %s:%d", funcName, file, line)
}

func Err(err error) error {
	if err == nil {
		return nil
	}
	return &Error{
		error: err,
		traceLine: TraceLine(1),
	}
}

func newError(err string, fmtargs ...any) *Error {
	e := &Error{
		err:       err,
		traceLine: TraceLine(2),
	}
	if len(fmtargs) > 0 {
		if str := fmtargs[0].(string); str != "" {
			e.error = fmt.Errorf(str, fmtargs[1:]...)
		}
	}
	return e
}

// Error returns the error message
func (e *Error) Error() string {
	if e.error != nil {
		if e.err != "" {
			return fmt.Sprintf("%v: %v", e.err, e.error)
		} else {
			return e.error.Error()
		}
	}
	return e.err
}

// Is returns true if the error is of the given type or subtype
func (e *Error) Is(err error) bool {
	if err, ok := err.(*Error); ok {
		return strings.HasPrefix(e.err, err.err)
	}
	return false
}

// Unwrap returns the underlying error
func (e *Error) Unwrap() error {
	return e.error
}

// TraceLine returns the line of code that caused the error
func (e *Error) TraceLine() string {
	return e.traceLine
}
