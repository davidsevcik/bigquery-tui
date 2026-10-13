package bq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	bqapi "cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
)

// Kind classifies a BigQuery failure for the retry and fallback logic.
type Kind int

const (
	KindOther      Kind = iota
	KindPermission      // access denied: do not retry, try the next fallback
	KindNotFound        // object is gone
	KindTransient       // quota, 5xx, network: retry with backoff
	KindOverLimit       // bytes-billed cap exceeded: do not retry
	KindAuth            // credentials missing or expired
	KindCancelled       // context cancelled or deadline exceeded by the caller
)

func (k Kind) String() string {
	return [...]string{"other", "permission", "not_found", "transient", "over_limit", "auth", "cancelled"}[k]
}

// Error wraps a failure with its classification.
type Error struct {
	Kind Kind
	Op   string
	Err  error
}

func (e *Error) Error() string {
	if e.Op == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Wrap classifies err (if it is not already classified) and attaches op.
func Wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	var be *Error
	if errors.As(err, &be) {
		if be.Op == "" && op != "" {
			return &Error{Kind: be.Kind, Op: op, Err: be.Err}
		}
		return err
	}
	return &Error{Kind: Classify(err), Op: op, Err: err}
}

// KindOf returns the classification of err.
func KindOf(err error) Kind {
	var be *Error
	if errors.As(err, &be) {
		return be.Kind
	}
	return Classify(err)
}

// Retryable reports whether the failure is worth retrying.
func Retryable(err error) bool { return KindOf(err) == KindTransient }

// Classify maps a raw client error to a Kind.
func Classify(err error) Kind {
	if err == nil {
		return KindOther
	}
	if errors.Is(err, context.Canceled) {
		return KindCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindTransient
	}
	reason := ""
	code := 0
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		code = ge.Code
		if len(ge.Errors) > 0 {
			reason = ge.Errors[0].Reason
		}
	}
	var be *bqapi.Error
	if errors.As(err, &be) {
		reason = be.Reason
	}
	switch reason {
	case "accessDenied", "forbidden":
		return KindPermission
	case "notFound":
		return KindNotFound
	case "bytesBilledLimitExceeded":
		return KindOverLimit
	case "rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded", "jobRateLimitExceeded",
		"backendError", "internalError", "serviceUnavailable", "timeout":
		return KindTransient
	case "invalidGrant", "authError":
		return KindAuth
	}
	switch {
	case code == 401:
		return KindAuth
	case code == 403:
		return KindPermission
	case code == 404:
		return KindNotFound
	case code == 429 || code >= 500:
		return KindTransient
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return KindTransient
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "invalid_grant"), strings.Contains(msg, "invalid_rapt"),
		strings.Contains(msg, "could not find default credentials"):
		return KindAuth
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "unexpected eof"),
		strings.Contains(msg, "tls handshake timeout"), strings.Contains(msg, "no such host"):
		return KindTransient
	}
	return KindOther
}
