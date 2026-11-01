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

// ImpersonationError is a refused service account impersonation: the IAM
// credentials API would not mint a token for the service account an
// impersonated_service_account ADC file names, because the source account
// lacks iam.serviceAccounts.getAccessToken on it or a new grant has not
// propagated yet. Logging in again does not fix it.
type ImpersonationError struct {
	ServiceAccount string // the target; empty when not known
	Err            error
}

func (e *ImpersonationError) Error() string {
	sa := e.ServiceAccount
	if sa == "" {
		sa = "the service account"
	}
	return "cannot impersonate " + sa + ": your account needs roles/iam.serviceAccountTokenCreator on it (new grants can take a minute)"
}

func (e *ImpersonationError) Unwrap() error { return e.Err }

// ImpersonationDenied reports whether err is a refused impersonation. Its
// message names the fix, so the ADC login hint does not apply.
func ImpersonationDenied(err error) bool {
	var ie *ImpersonationError
	return errors.As(err, &ie) || refusedImpersonation(err)
}

// refusedImpersonation matches the raw failure. golang.org/x/oauth2 and
// cloud.google.com/go/auth both turn the IAM credentials answer into a plain
// error, "status code 403: <JSON body>", with no type to match.
func refusedImpersonation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status code 403: ") && strings.Contains(msg, "iam.serviceAccounts.getAccessToken")
}

// asImpersonation returns err as an *ImpersonationError when it is a refused
// impersonation, naming the target from the ADC file adc returns; nil
// otherwise.
func asImpersonation(err error, adc func() []byte) *ImpersonationError {
	var ie *ImpersonationError
	if errors.As(err, &ie) {
		return ie
	}
	if !refusedImpersonation(err) {
		return nil
	}
	return &ImpersonationError{ServiceAccount: impersonationTarget(adc()), Err: err}
}

var impersonationURL = regexp.MustCompile(`/serviceAccounts/([^/:]+):generateAccessToken`)

// impersonationTarget returns the service account an ADC file impersonates,
// from its service_account_impersonation_url; "" when there is none. Nothing
// else is read from the file.
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

// defaultADC returns the ADC file's JSON, or nil. It runs only after a
// refused impersonation, so the file exists and no metadata server is probed.
func defaultADC() []byte {
	creds, err := google.FindDefaultCredentials(context.Background())
	if err != nil {
		return nil
	}
	return creds.JSON
}
