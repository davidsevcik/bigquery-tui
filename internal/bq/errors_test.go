package bq

import (
	"context"
	"errors"
	"fmt"
	"testing"

	bqapi "cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
)

func gerr(code int, reason string) error {
	return &googleapi.Error{Code: code, Errors: []googleapi.ErrorItem{{Reason: reason}}}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want Kind
	}{
		{gerr(403, "accessDenied"), KindPermission},
		{gerr(403, "forbidden"), KindPermission},
		{gerr(403, "rateLimitExceeded"), KindTransient},
		{gerr(403, "quotaExceeded"), KindTransient},
		{gerr(403, ""), KindPermission},
		{gerr(404, "notFound"), KindNotFound},
		{gerr(404, ""), KindNotFound},
		{gerr(429, ""), KindTransient},
		{gerr(500, "backendError"), KindTransient},
		{gerr(503, ""), KindTransient},
		{gerr(401, ""), KindAuth},
		{&bqapi.Error{Reason: "accessDenied", Message: "Access Denied"}, KindPermission},
		{&bqapi.Error{Reason: "bytesBilledLimitExceeded"}, KindOverLimit},
		{&bqapi.Error{Reason: "invalidQuery"}, KindOther},
		{context.Canceled, KindCancelled},
		{fmt.Errorf("wrapped: %w", context.Canceled), KindCancelled},
		{context.DeadlineExceeded, KindTransient},
		{errors.New("oauth2: cannot fetch token: invalid_grant"), KindAuth},
		{errors.New("read tcp: connection reset by peer"), KindTransient},
		{errors.New("something else"), KindOther},
	}
	for _, c := range cases {
		if got := Classify(c.err); got != c.want {
			t.Errorf("%v: got %v, want %v", c.err, got, c.want)
		}
	}
}

func TestWrapKeepsKind(t *testing.T) {
	inner := Wrap("inner", gerr(403, "accessDenied"))
	outer := Wrap("outer", inner)
	if KindOf(outer) != KindPermission {
		t.Fatal("kind must survive re-wrapping")
	}
	var be *Error
	if !errors.As(outer, &be) || be.Op != "inner" {
		t.Fatalf("an already-classified error keeps its op: %+v", be)
	}
	if Wrap("x", nil) != nil {
		t.Fatal("Wrap(nil) is nil")
	}
	if !Retryable(Wrap("x", gerr(503, ""))) || Retryable(Wrap("x", gerr(403, "accessDenied"))) {
		t.Fatal("Retryable follows Kind")
	}
}

func TestEndpointOptionsComeFromTheEnvironment(t *testing.T) {
	t.Setenv(EndpointEnv, "")
	if o := EndpointOptions(); o != nil {
		t.Fatalf("unset: %v", o)
	}
	t.Setenv(EndpointEnv, " http://localhost:9050 ")
	if o := EndpointOptions(); len(o) != 2 {
		t.Fatalf("an endpoint and no credentials: %v", o)
	}
}
