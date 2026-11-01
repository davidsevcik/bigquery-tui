package bq

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/api/option"
)

const deniedSA = "bqt-reader@bqt-sandbox.iam.gserviceaccount.com"

// deniedBody is what iamcredentials generateAccessToken answers when the
// source account lacks iam.serviceAccounts.getAccessToken on the target.
const deniedBody = `{
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

// impersonationDenied serves a user token endpoint and an IAM credentials
// endpoint that refuses to impersonate, and points ADC at an
// impersonated_service_account file using them. It returns the server URL.
func impersonationDenied(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"source-token","token_type":"Bearer","expires_in":3600}`))
		case strings.HasSuffix(r.URL.Path, ":generateAccessToken"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(deniedBody))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	// A service account source, because both auth libraries honour its
	// token_uri (cloud.google.com/go/auth ignores it for authorized_user and
	// would call Google).
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := json.Marshal(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
	t.Setenv("GOOGLE_AUTH_TRUST_BOUNDARY_ENABLED", "")
	adc := `{
  "type": "impersonated_service_account",
  "service_account_impersonation_url": "` + srv.URL + `/v1/projects/-/serviceAccounts/` + deniedSA + `:generateAccessToken",
  "delegates": [],
  "source_credentials": {
    "type": "service_account",
    "client_email": "source@bqt-sandbox.iam.gserviceaccount.com",
    "private_key": ` + string(key) + `,
    "token_uri": "` + srv.URL + `/token"
  }
}`
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, []byte(adc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	return srv.URL
}

const deniedMsg = "cannot impersonate " + deniedSA + ": your account needs roles/iam.serviceAccountTokenCreator on it (new grants can take a minute)"

func TestCheckAuthNamesARefusedImpersonation(t *testing.T) {
	impersonationDenied(t)
	info, err := CheckAuth(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if got, want := err.Error(), "fetch access token: "+deniedMsg; got != want {
		t.Fatalf("message\n got %q\nwant %q", got, want)
	}
	if KindOf(err) != KindAuth || !ImpersonationDenied(err) {
		t.Fatalf("kind %v, impersonation %v", KindOf(err), ImpersonationDenied(err))
	}
	if info.Type != "impersonated_service_account" {
		t.Fatalf("type %q", info.Type)
	}
	if qe := QueryErrorOf(err); qe.Kind != "auth" || qe.Hint != "" || qe.Message != deniedMsg {
		t.Fatalf("query error %+v: the message says the fix, the ADC login hint does not apply", qe)
	}
}

func TestListProjectsNamesARefusedImpersonation(t *testing.T) {
	url := impersonationDenied(t)
	ctx := context.Background()
	c, err := NewClient(ctx, "", Options{ExtraClientOptions: []option.ClientOption{option.WithEndpoint(url + "/bigquery/v2/")}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.ListProjects(ctx)
	if err == nil {
		t.Fatal("want an error")
	}
	if got, want := err.Error(), "list projects: "+deniedMsg; got != want {
		t.Fatalf("message\n got %q\nwant %q", got, want)
	}
	if KindOf(err) != KindAuth || Retryable(err) || !ImpersonationDenied(err) {
		t.Fatalf("kind %v", KindOf(err))
	}
}

func TestImpersonationDeniedWithoutTheTarget(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	err := Wrap("op", &ImpersonationError{Err: os.ErrPermission})
	if !strings.Contains(err.Error(), "op: cannot impersonate the service account: ") {
		t.Fatal(err)
	}
	if ImpersonationDenied(Wrap("op", gerr(403, "accessDenied"))) {
		t.Fatal("a BigQuery access denial is not an impersonation failure")
	}
}
