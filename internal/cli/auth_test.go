package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

const impersonatedSA = "bqt-reader@bqt-sandbox.iam.gserviceaccount.com"

// deniedJSON is the IAM credentials answer when the source account lacks
// iam.serviceAccounts.getAccessToken on the impersonated service account.
const deniedJSON = `{
  "error": {
    "code": 403,
    "message": "Permission 'iam.serviceAccounts.getAccessToken' denied on resource (or it may not exist).",
    "status": "PERMISSION_DENIED",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "IAM_PERMISSION_DENIED",
        "domain": "iam.googleapis.com",
        "metadata": {
          "permission": "iam.serviceAccounts.getAccessToken"
        }
      }
    ]
  }
}
`

const deniedLine = "cannot impersonate " + impersonatedSA + ": your account needs roles/iam.serviceAccountTokenCreator on it (new grants can take a minute)"

// missingJSON is the IAM credentials answer for a target that does not exist.
const missingJSON = `{
  "error": {
    "code": 404,
    "message": "Not found; Gaia id not found for email ` + impersonatedSA + `",
    "status": "NOT_FOUND"
  }
}
`

const missingLine = "cannot impersonate " + impersonatedSA + ": no such service account (check service_account_impersonation_url in the credentials file)"

// refusals are the ways generateAccessToken refuses: status, body, and the
// one line bqt shows.
var refusals = []struct {
	name   string
	status int
	body   string
	line   string
}{
	{"denied", 403, deniedJSON, deniedLine},
	{"missing", 404, missingJSON, missingLine},
}

// impersonatedADC points ADC at an impersonated_service_account file, so the
// refused target can be named.
func impersonatedADC(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adc.json")
	adc := `{"type": "impersonated_service_account",
  "service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/` + impersonatedSA + `:generateAccessToken",
  "source_credentials": {"type": "authorized_user", "client_id": "id", "client_secret": "secret", "refresh_token": "refresh"}}`
	if err := os.WriteFile(path, []byte(adc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
}

// The errors as the auth libraries build them (internal/bq drives the real
// libraries): golang.org/x/oauth2 for `bqt doctor`, cloud.google.com/go/auth
// under the BigQuery client's http.Client for every API call.
func oauth2Refused(status int, body string) error {
	return bq.Wrap("fetch access token", fmt.Errorf("oauth2/google: status code %d: %s", status, body))
}

func clientRefused(op string, status int, body string) error {
	return bq.Wrap(op, &url.Error{Op: "Get", URL: "https://bigquery.googleapis.com/bigquery/v2/projects?alt=json",
		Err: fmt.Errorf("credentials: status code %d: %s", status, body)})
}

func TestDoctorNamesARefusedImpersonation(t *testing.T) {
	impersonatedADC(t)
	for _, rf := range refusals {
		t.Run(rf.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig("projects = [\"proj-a\"]\n")
			s.authErr = oauth2Refused(rf.status, rf.body)
			code, out, _ := s.run("doctor", "--format", "text")
			if code != 0 {
				t.Fatal(code)
			}
			if !strings.Contains(out, "\n          fetch access token: "+rf.line+"\n") {
				t.Fatalf("want the one-line reason:\n%s", out)
			}
			if strings.Contains(out, "application-default login") || strings.Contains(out, `"status"`) {
				t.Fatalf("no raw JSON and no login hint:\n%s", out)
			}
		})
	}

	// an expired login keeps the hint
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	s.authErr = bq.Wrap("fetch access token", fmt.Errorf("oauth2: cannot fetch token: invalid_grant"))
	if _, out, _ := s.run("doctor", "--format", "text"); !strings.Contains(out, "application-default login") {
		t.Fatalf("want the login hint:\n%s", out)
	}
}

func TestProjectsNamesARefusedImpersonation(t *testing.T) {
	impersonatedADC(t)
	for _, rf := range refusals {
		t.Run(rf.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig("")
			s.fake.Failures = append(s.fake.Failures, &bqtest.Failure{Method: "ListProjects", Err: clientRefused("list projects", rf.status, rf.body)})
			_, out, _ := s.run("projects", "--format", "text")
			if !strings.Contains(out, "could not list your projects: list projects: "+rf.line+"\n") || strings.Contains(out, `"status"`) {
				t.Fatalf("want the one-line reason:\n%s", out)
			}
		})
	}
}

func TestSyncRefusedImpersonationHasNoLoginHint(t *testing.T) {
	impersonatedADC(t)
	for _, rf := range refusals {
		t.Run(rf.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig("projects = [\"proj-a\"]\n")
			s.fake.Projects["proj-a"].ListErr = clientRefused("list datasets of proj-a", rf.status, rf.body)
			code, _, errOut := s.run("sync")
			if code != api.ExitAuth || strings.Contains(errOut, "application-default login") {
				t.Fatalf("%d %s", code, errOut)
			}
		})
	}
}

func TestClassifyRefusedImpersonation(t *testing.T) {
	impersonatedADC(t)
	for _, rf := range refusals {
		ce := classify(clientRefused("list projects", rf.status, rf.body))
		if ce.exit != api.ExitAuth || ce.code != "auth" || ce.msg != "list projects: "+rf.line || ce.hint != "" {
			t.Fatalf("%s: %+v", rf.name, ce)
		}
	}
	// an expired login keeps the hint
	if ce := classify(bq.Wrap("list projects", fmt.Errorf("oauth2: cannot fetch token: invalid_grant"))); !strings.Contains(ce.hint, "application-default login") {
		t.Fatalf("%+v", ce)
	}
	// a missing BigQuery object is not a missing service account
	if ce := classify(bq.Wrap("get table", fmt.Errorf("googleapi: Error 404: Not found: Table p:d.t, notFound"))); ce.code == "auth" {
		t.Fatalf("%+v", ce)
	}
}
