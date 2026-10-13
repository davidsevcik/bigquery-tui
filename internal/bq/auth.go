package bq

import (
	"context"
	"encoding/json"

	"golang.org/x/oauth2/google"
)

// AuthInfo describes the Application Default Credentials without exposing
// any secret.
type AuthInfo struct {
	Type         string `json:"type"`                    // authorized_user, service_account, ...
	ClientEmail  string `json:"client_email,omitempty"`  // service accounts only
	QuotaProject string `json:"quota_project,omitempty"` // as stored in the ADC file
	ProjectID    string `json:"project_id,omitempty"`
}

// CheckAuth finds the default credentials and fetches a token, reporting
// whether they work. It never returns or logs the token.
func CheckAuth(ctx context.Context) (AuthInfo, error) {
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/bigquery")
	if err != nil {
		return AuthInfo{}, Wrap("find default credentials", err)
	}
	var meta struct {
		Type         string `json:"type"`
		ClientEmail  string `json:"client_email"`
		QuotaProject string `json:"quota_project_id"`
	}
	_ = json.Unmarshal(creds.JSON, &meta)
	info := AuthInfo{Type: meta.Type, ClientEmail: meta.ClientEmail, QuotaProject: meta.QuotaProject, ProjectID: creds.ProjectID}
	if info.Type == "" {
		info.Type = "metadata_server_or_other"
	}
	if _, err := creds.TokenSource.Token(); err != nil {
		return info, Wrap("fetch access token", err)
	}
	return info, nil
}
