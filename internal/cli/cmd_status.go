package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/logging"
	"github.com/davidsevcik/bigquery-tui/internal/store"
	"github.com/davidsevcik/bigquery-tui/internal/version"
)

type engineLog struct{ *slog.Logger }

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func cmdStatus(ctx context.Context, o *out, g *globals, args []string) error {
	if err := parseNoArgs(commandFlags("status", g), args); err != nil {
		return err
	}
	env := o.env
	cfg, err := load(env, g)
	if err != nil {
		return err
	}
	eng, err := engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Now: env.Now})
	if err != nil {
		return err
	}
	defer eng.Close()
	st, err := eng.Status(ctx)
	if err != nil {
		return err
	}
	o.result(st, func(w io.Writer) { printStatus(w, st) })
	return nil
}

func ago(now time.Time, t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := now.Sub(*t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func printStatus(w io.Writer, st api.CatalogStatus) {
	fmt.Fprintf(w, "catalog: %d datasets, %d tables, %d columns\n", st.Datasets, st.Tables, st.Columns)
	if st.SyncRunning {
		fmt.Fprintln(w, "a sync is running")
	}
	if st.SyncNeeded {
		fmt.Fprintf(w, "sync needed: %s\n", st.SyncReason)
	}
	if st.ExecutionProject != "" {
		fmt.Fprintf(w, "execution project: %s (%s)\n", st.ExecutionProject, st.ExecutionSource)
	}
	if len(st.Projects) == 0 {
		fmt.Fprintln(w, "no projects selected (bqt projects select ID)")
	}
	for _, p := range st.Projects {
		mark := "ok   "
		if p.Stale {
			mark = "stale"
		}
		note := ""
		if !p.Selected {
			note = "  (not selected)"
		}
		fmt.Fprintf(w, "%s %-32s synced %s%s\n", mark, p.Project, ago(st.Now, p.SyncedAt), note)
		if p.Pending > 0 {
			fmt.Fprintf(w, "       %d dataset(s) listed but not synced yet (no tables in the catalog)\n", p.Pending)
		}
		for _, u := range p.Units {
			loc := u.Location
			if loc == "" {
				loc = "(discovery)"
			}
			fmt.Fprintf(w, "       %-14s %-12s %d datasets, %d tables, synced %s\n", loc, u.Status, u.Datasets, u.Tables, ago(st.Now, u.LastSuccess))
			if u.Error != "" {
				fmt.Fprintf(w, "         %s\n", u.Error)
			}
		}
	}
}

func cmdDoctor(ctx context.Context, o *out, g *globals, args []string) error {
	if err := parseNoArgs(commandFlags("doctor", g), args); err != nil {
		return err
	}
	env := o.env
	rep := api.DoctorReport{
		Version: version.String(), GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		ConfigPath: config.ResolvePath(g.loadOptions(env)),
	}
	cfg, cerr := load(env, g)
	if cerr != nil {
		rep.ConfigError = cerr.Error()
	} else {
		rep.ConfigOK = true
		p := store.PathsFor(cfg.DataDir)
		rep.DataDir = cfg.DataDir
		rep.LogPath = p.LogDir + string(os.PathSeparator) + logging.FileName
		if _, err := os.Stat(rep.LogPath); errors.Is(err, fs.ErrNotExist) {
			rep.LogMissing = true
		} else if err != nil {
			rep.LogError = err.Error()
		}
		if _, err := os.Stat(p.History); err == nil {
			rep.History.Exists = true
		}
		if eng, err := engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Now: env.Now}); err == nil {
			rep.Execution, rep.ExecutionSource = eng.ExecutionProject()
			rep.Projects = len(eng.Selection().Selected)
			if st, err := eng.Status(ctx); err == nil {
				rep.SyncRunning = st.SyncRunning
				rep.Catalog = api.DoctorCatalog{Exists: eng.Catalog() != nil, Tables: st.Tables, Datasets: st.Datasets, SyncNeeded: st.SyncNeeded}
				var newest *time.Time
				for _, pr := range st.Projects {
					if pr.SyncedAt != nil && (newest == nil || pr.SyncedAt.After(*newest)) {
						t := *pr.SyncedAt
						newest = &t
					}
				}
				if newest != nil {
					rep.Catalog.LastSync = newest
					rep.Catalog.Age = env.Now().Sub(*newest).Round(time.Second).String()
				}
			}
			eng.Close()
		}
	}
	check := env.CheckAuth
	if check == nil {
		check = bq.CheckAuth
	}
	info, aerr := check(ctx)
	rep.Auth = api.DoctorAuth{OK: aerr == nil, Type: info.Type, ClientEmail: info.ClientEmail, QuotaProject: info.QuotaProject}
	if aerr != nil {
		rep.Auth.Error = aerr.Error()
	}
	hint := aerr != nil && !bq.ImpersonationDenied(aerr)
	o.result(rep, func(w io.Writer) { printDoctor(w, rep, hint) })
	return nil
}

// printDoctor prints the report; withHint adds the ADC login hint under an
// auth error.
func printDoctor(w io.Writer, r api.DoctorReport, withHint bool) {
	yn := func(b bool) string {
		if b {
			return "ok"
		}
		return "PROBLEM"
	}
	fmt.Fprintf(w, "bqt %s (%s, %s)\n", r.Version, r.GoVersion, r.Platform)
	fmt.Fprintf(w, "config:   %s [%s]\n", r.ConfigPath, yn(r.ConfigOK))
	if r.ConfigError != "" {
		fmt.Fprintf(w, "          %s\n", r.ConfigError)
	}
	if r.ConfigOK {
		exec := r.Execution
		if exec == "" {
			exec = "none (select a project: `bqt projects select ID`)"
		} else if r.ExecutionSource != "" {
			exec += " (" + string(r.ExecutionSource) + ")"
		}
		fmt.Fprintf(w, "execution project: %s, %d project(s) selected\n", exec, r.Projects)
		logStatus := r.LogPath
		switch {
		case r.LogMissing:
			logStatus += " (missing)"
		case r.LogError != "":
			logStatus += " (cannot check: " + r.LogError + ")"
		}
		fmt.Fprintf(w, "data dir: %s\nlog file: %s\n", r.DataDir, logStatus)
		if r.Catalog.Exists {
			age := "never synced"
			if r.Catalog.LastSync != nil {
				age = "last sync " + r.Catalog.Age + " ago"
			}
			fmt.Fprintf(w, "catalog:  %d datasets, %d tables, %s\n", r.Catalog.Datasets, r.Catalog.Tables, age)
		} else {
			fmt.Fprintln(w, "catalog:  none yet (run `bqt sync`)")
		}
	}
	auth := yn(r.Auth.OK)
	if r.Auth.Type != "" {
		auth += ", " + r.Auth.Type
	}
	if r.Auth.ClientEmail != "" {
		auth += ", " + r.Auth.ClientEmail
	}
	fmt.Fprintf(w, "auth:     %s\n", auth)
	if r.Auth.Error != "" {
		fmt.Fprintf(w, "          %s\n", r.Auth.Error)
	}
	if withHint {
		fmt.Fprintln(w, "          "+loginHint)
	}
}
