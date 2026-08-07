package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/mitchellh/mapstructure"
)

type Nil struct {}

func IsNilType(v any) bool {
	_, isConcrete := v.(Nil)
	_, isPtr := v.(*Nil)
	return isConcrete || isPtr
}

type Request[Query any, Body any] struct {
	Query Query
	Body  Body
	R *http.Request
}

type Requester[Q any, B any] func(ctx context.Context, request Request[Q, B]) error

func HandleRequester[Q any, B any](fn Requester[Q, B]) http.HandlerFunc {
	return HandleError(func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()

		query := new(Q)
		if !IsNilType(query) {
			if err := readParams(r, query); err != nil {
				return err
			}
		}

		// Read body
		body := new(B)
		if !IsNilType(body) {
			if err := readJSON(r, body); err != nil {
				return err
			}
		}

		err := fn(ctx, Request[Q, B]{
			Query: *query,
			Body:  *body,
			R: r,
		})
		if err != nil {
			return err
		}

		w.WriteHeader(http.StatusOK)
		return nil
	})
}

type RequestReturner[Q any, B any, R any] func(ctx context.Context, request Request[Q, B]) (R, error)

func HandleRequestReturner[Q any, B any, R any](fn RequestReturner[Q, B, R]) http.HandlerFunc {
	return HandleError(func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()

		query := new(Q)
		if !IsNilType(query) {
			if err := readParams(r, query); err != nil {
				return err
			}
		}

		// Read body
		body := new(B)
		if !IsNilType(body) {
			if err := readJSON(r, body); err != nil {
				return err
			}
		}

		ret, err := fn(ctx, Request[Q, B]{
			Query: *query,
			Body:  *body,
			R: r,
		})
		if err != nil {
			return err
		}

		return writeJSON(w, ret)
	})
}

/*
type RequestAuther[Q, B any] func(ctx context.Context, request service.RequestAuth[Q, B]) error

func HandleRequestAuther[Q, B any](fn RequestAuther[Q, B]) http.HandlerFunc {
	return HandleError(func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		auth := slidefy.AuthStateFrom(ctx)

		if auth == nil {
			return slidefy.ErrAuth("not authorized")
		}

		query := new(Q)
		if !service.IsNilType(query) {
			if err := readParams(r, query); err != nil {
				return slidefy.ErrBadRequest("parse request params: %w", err)
			}
		}

		// Read body
		body := new(B)
		if !service.IsNilType(body) {
			if err := readJSON(r, body); err != nil {
				return slidefy.ErrBadRequest("parse request body: %w", err)
			}
		}

		err := fn(ctx, service.RequestAuth[Q, B]{
			Auth:  *auth,
			Query: *query,
			Body:  *body,
		})
		if err != nil {
			return err
		}

		w.WriteHeader(http.StatusOK)
		return nil
	})
}

type RequestAuthReturner[Q, B, R any] func(ctx context.Context, request service.RequestAuth[Q, B]) (R, error)

func HandleRequestAuthReturner[Q, B, R any](fn RequestAuthReturner[Q, B, R]) http.HandlerFunc {
	return HandleError(func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		auth := slidefy.AuthStateFrom(ctx)

		if auth == nil {
			return slidefy.ErrAuth("not authorized")
		}

		query := new(Q)
		if !service.IsNilType(query) {
			if err := readParams(r, query); err != nil {
				return slidefy.ErrBadRequest("parse request params: %w", err)
			}
		}

		// Read body
		body := new(B)
		if !service.IsNilType(body) {
			if err := readJSON(r, body); err != nil {
				return slidefy.ErrBadRequest("parse request body: %w", err)
			}
		}

		ret, err := fn(ctx, service.RequestAuth[Q, B]{
			Auth:  *auth,
			Query: *query,
			Body:  *body,
		})
		if err != nil {
			return err
		}

		return writeJSON(w, ret)
	})
}

type RequestCustomResponser[Q, B any] func(ctx context.Context, request service.Request[Q, B], w http.ResponseWriter) error

func HandleRequestCustomResponser[Q, B any](fn RequestCustomResponser[Q, B]) http.HandlerFunc {
	return HandleError(func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		auth := slidefy.AuthStateFrom(ctx)

		query := new(Q)
		if !service.IsNilType(query) {
			if err := readParams(r, query); err != nil {
				return slidefy.ErrBadRequest("parse request params: %w", err)
			}
		}

		// Read body
		body := new(B)
		if !service.IsNilType(body) {
			if err := readJSON(r, body); err != nil {
				return slidefy.ErrBadRequest("parse request body: %w", err)
			}
		}

		err := fn(ctx, service.Request[Q, B]{
			Auth:  auth,
			Query: *query,
			Body:  *body,
		}, w)
		if err != nil {
			return err
		}

		w.WriteHeader(200)
		return nil
	})
}

type RequestAuthCustomResponser[Q, B any] func(ctx context.Context, request service.RequestAuth[Q, B], w http.ResponseWriter) error

func HandleRequestAuthCustomResponser[Q, B any](fn RequestAuthCustomResponser[Q, B]) http.HandlerFunc {
	return HandleError(func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		auth := slidefy.AuthStateFrom(ctx)

		if auth == nil {
			return slidefy.ErrAuth("not authorized")
		}

		query := new(Q)
		if !service.IsNilType(query) {
			if err := readParams(r, query); err != nil {
				return slidefy.ErrBadRequest("parse request params: %w", err)
			}
		}

		// Read body
		body := new(B)
		if !service.IsNilType(body) {
			if err := readJSON(r, body); err != nil {
				return slidefy.ErrBadRequest("parse request body: %w", err)
			}
		}

		err := fn(ctx, service.RequestAuth[Q, B]{
			Auth:  *auth,
			Query: *query,
			Body:  *body,
		}, w)
		if err != nil {
			return err
		}

		w.WriteHeader(200)
		return nil
	})
}
*/

// writeJSON is a helper function that sets the Content-Type header and writes the JSON response.
func writeJSON(w http.ResponseWriter, data any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	err := json.NewEncoder(w).Encode(data)
	if err != nil {
		return ErrSendResponse("%w", err)
	}
	return nil
}

// readJSON is a helper function that reads the JSON request body.
func readJSON(r *http.Request, data any) error {
	err := json.NewDecoder(r.Body).Decode(data)
	if err != nil {
		return ErrBadRequest("decode request body: %w", err)
	}
	return nil
}

// readParams is a helper function that reads the Query and URL params
func readParams(r *http.Request, data any) error {
	t := reflect.TypeOf(data)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	fields := reflect.VisibleFields(t)
	query := r.URL.Query()

	vals := map[string]any{}

	for _, field := range fields {
		source := field.Tag.Get("url")

		name := strings.ToLower(field.Name)
		if tag := field.Tag.Get("name"); tag != "" {
			name = tag
		}

		v := ""
		switch source {
		case "param":
			v = r.PathValue(name)
		case "query":
			v = query.Get(name)
		default:
			continue
		}
		if v != "" {
			vals[field.Name] = v
		}
		fmt.Println(v, name, field.Name)
	}

	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result: data,

		// Empties slices before writing to them
		ZeroFields: true,

		// Squash embedded structs
		Squash: true,

		// Converts strings to ids
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.TextUnmarshallerHookFunc(),
			func(from reflect.Type, to reflect.Type, v any) (any, error) {
				if from == reflect.TypeOf("") && to == reflect.TypeOf(false) {
					value := v.(string)
					if value == "true" {
						return true, nil
					}
					if value == "false" {
						return false, nil
					}
					return false, ErrBadRequest("unable to decode bool")
				}
				return v, nil
			}),
	})
	if err != nil {
		return ErrInternal("create decoder: %s", err)
	}
	if err := dec.Decode(vals); err != nil {
		return ErrBadRequest("decode params: %s", err)
	}
	return nil
}
