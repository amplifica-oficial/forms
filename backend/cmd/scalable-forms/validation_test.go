package main

import (
	"fmt"
	"testing"
	"time"
)

func TestValidatePutFormReq(t *testing.T) {
	tt := []struct{
		value PutFormReq
		expect string
	}{
		{PutFormReq{Slug: "aa-bb"}, "target"},
		{PutFormReq{Slug: "aa-bb", Target: "ftp://example.com"}, "target"},
		{PutFormReq{Slug: "aa-bb", Target: "https://example.com"}, ""},
		{PutFormReq{Slug: "aa-bb", Target: "https://example.com", Open: new(time.Now()), Close: new(time.Now().Add(-time.Second))}, "time"},
		{PutFormReq{Slug: "aa-bb", Target: "https://example.com", Open: new(time.Now()), Close: new(time.Now().Add(time.Second))}, ""},
	}

	for i, tc := range tt {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			v := tc.value.Validate()
			if v != tc.expect {
				t.Errorf("expect: %s; got: %s", tc.expect, v)
			}
		})
	}
}


func TestValidateCreateResponseReq(t *testing.T) {
	tt := []struct{
		value CreateResponseReq
		expect string
	}{
		{CreateResponseReq{Slug: "aa-bb"}, "e-mail"},
		{CreateResponseReq{Slug: "aa-bb", Email: "aaaa@example.com"}, "name"},
		{CreateResponseReq{Slug: "aa-bb", Email: "aaaa@example.com", Name: " BAD NAME"}, "name"},
		{CreateResponseReq{Slug: "aa-bb", Email: "aaaa@example.com", Name: "BAD NAME "}, "name"},
		{CreateResponseReq{Slug: "aa-bb", Email: "aaaa@example.com", Name: "B"}, ""},
		{CreateResponseReq{Slug: "aa-bb", Email: " aaaa@example.com", Name: "OK NAME"}, "e-mail"},
		{CreateResponseReq{Slug: "aa-bb", Email: "aaaa@example.com ", Name: "OK NAME"}, "e-mail"},
		{CreateResponseReq{Slug: "aa-bb", Email: "aaaa@example.com (lal lalal)", Name: "OK NAME"}, "e-mail"},
	}

	for i, tc := range tt {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			v := tc.value.Validate()
			if v != tc.expect {
				t.Errorf("expect: %s; got: %s", tc.expect, v)
			}
		})
	}
}

