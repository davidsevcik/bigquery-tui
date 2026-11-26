package bq

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/oauth2/google"
)

type ImpersonationError struct {
	ServiceAccount string
	NotFound       bool
	Err            error
}

func (e *ImpersonationError) Error() string {
	sa := e.ServiceAccount
	if sa == "" {
		sa = "the service account"
	}
	if e.NotFound {
		return "cannot impersonate " + sa + ": no such service account (check service_account_impersonation_url in the credentials file)"
	}
	return "cannot impersonate " + sa + ": your account needs roles/iam.serviceAccountTokenCreator on it (new grants can take a minute)"
}

func (e *ImpersonationError) Unwrap() error { return e.Err }

func ImpersonationDenied(err error) bool {
	var ie *ImpersonationError
	return errors.As(err, &ie) || refusedImpersonation(err)
}

func refusedImpersonation(err error) bool {
	return deniedImpersonation(err) || missingImpersonationTarget(err)
}

func deniedImpersonation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status code 403: ") && strings.Contains(msg, "iam.serviceAccounts.getAccessToken")
}

func missingImpersonationTarget(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status code 404: ") && strings.Contains(msg, "Gaia id not found")
}

func asImpersonation(err error, adc func() []byte) *ImpersonationError {
	var ie *ImpersonationError
	if errors.As(err, &ie) {
		return ie
	}
	if !refusedImpersonation(err) {
		return nil
	}
	return &ImpersonationError{ServiceAccount: impersonationTarget(adc()), NotFound: missingImpersonationTarget(err), Err: err}
}

var impersonationURL = regexp.MustCompile(`/serviceAccounts/([^/:]+):generateAccessToken`)

func impersonationTarget(adc []byte) string {
	var f struct {
		URL string `json:"service_account_impersonation_url"`
	}
	if json.Unmarshal(adc, &f) != nil {
		return ""
	}
	m := impersonationURL.FindStringSubmatch(f.URL)
	if m == nil {
		return ""
	}
	sa, err := url.PathUnescape(m[1])
	if err != nil {
		return ""
	}
	return sa
}

// ADR-0005
func defaultADC() []byte {
	creds, err := google.FindDefaultCredentials(context.Background())
	if err != nil {
		return nil
	}
	return creds.JSON
}
