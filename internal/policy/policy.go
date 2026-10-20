// Package policy is the safety model of the agent entry points (spec 14.3):
// `bqt query`, `bqt favorites run` and `bqt mcp`. bqt cannot tell an agent
// from a person at the command line, so every non-TUI query goes through the
// same profile: SELECT only (unless the config allows CLI writes), a byte
// limit checked on the dry-run, a timeout, and caps on rows and cell size.
//
// The profile is a pure function of the config and the dry-run, so every
// frontend (and a future GUI's agent mode) gets the same answer. Flags and
// tool arguments can only lower a limit; raising one takes the config file.
package policy

import (
	"fmt"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
)

// Row and cell caps of the agent surface.
const (
	// DefaultRows is how many rows a query returns unless asked otherwise.
	DefaultRows = 100
	// MaxRows is the most rows one call returns; the rest is paged.
	MaxRows = 1000
	// CellChars is where a cell's text is cut.
	CellChars = 200
)

// Profile is the set of limits one agent query runs under.
type Profile struct {
	// Source is the entry point: cli or mcp.
	Source string `json:"source"`
	// MaxBytes is the most bytes the dry-run may estimate (agent_max_bytes,
	// or lower).
	MaxBytes int64 `json:"max_bytes"`
	// Timeout bounds the whole query, dry-run included (agent_timeout, or
	// shorter).
	Timeout time.Duration `json:"timeout"`
	// AllowWrites lets statements other than SELECT run: only from the CLI,
	// and only with cli_allow_writes = true.
	AllowWrites bool `json:"allow_writes"`
}

// For returns the profile of an entry point (cli or mcp) under cfg.
func For(cfg *config.Config, source string) Profile {
	p := Profile{Source: source, MaxBytes: cfg.AgentMaxBytes, Timeout: cfg.AgentTimeout,
		AllowWrites: source == "cli" && cfg.CLIAllowWrites}
	if p.MaxBytes <= 0 {
		p.MaxBytes = config.DefaultAgentMaxBytes
	}
	if p.Timeout <= 0 {
		p.Timeout = config.DefaultAgentTimeout
	}
	return p
}

// Lower returns the profile with its limits lowered to maxBytes and timeout
// where those are positive and lower. A higher value is ignored: raising a
// limit takes the config file.
func (p Profile) Lower(maxBytes int64, timeout time.Duration) Profile {
	if maxBytes > 0 && maxBytes < p.MaxBytes {
		p.MaxBytes = maxBytes
	}
	if timeout > 0 && timeout < p.Timeout {
		p.Timeout = timeout
	}
	return p
}

// AutoConfirmBytes is what confirm_bytes = auto means for an on-demand Run.
const AutoConfirmBytes int64 = 10 << 30

// ConfirmThreshold is the confirm_bytes threshold for a Run whose dry-run
// did (capacity) or did not show a reservation; 0 means none. The TUI asks
// above it; the agent entry points refuse.
func ConfirmThreshold(cfg *config.Config, capacity bool) int64 {
	switch cfg.ConfirmBytes.Mode {
	case config.ByteLimitValue:
		return cfg.ConfirmBytes.Bytes
	case config.ByteLimitAuto:
		if !capacity {
			return AutoConfirmBytes
		}
	}
	return 0
}

// Check returns why the profile refuses a query with this dry-run estimate,
// or nil when it may run. It fails closed: an empty or unknown statement
// type and an UNKNOWN estimate are refused. A LOWER_BOUND estimate is checked
// against its lower bound; the job's MaximumBytesBilled (max_bytes_billed,
// set on every job) is the server-side backstop. The agent limit is not set
// as the job's cap: BigQuery bills at least 10 MiB per table referenced, so a
// cap that low would refuse queries whose estimate is tiny.
func (p Profile) Check(cfg *config.Config, est api.Estimate) *api.Refusal {
	st := est.StatementType
	switch {
	case st == "":
		return &api.Refusal{Code: api.RefuseNotReadOnly, Message: "BigQuery did not report the statement type, so bqt cannot tell that the query only reads"}
	case st != "SELECT" && !p.AllowWrites:
		msg := fmt.Sprintf("%s statements are not allowed from %s: only SELECT runs here (run it in the TUI", st, p.where())
		if p.Source == "cli" {
			msg += ", or set cli_allow_writes = true in the config"
		}
		return &api.Refusal{Code: api.RefuseNotReadOnly, Message: msg + ")", StatementType: st}
	}
	switch est.Accuracy {
	case "", "PRECISE", "LOWER_BOUND", "UPPER_BOUND":
	default:
		return &api.Refusal{Code: api.RefuseUnknownEstimate, StatementType: st, Bytes: est.Bytes, Limit: p.MaxBytes,
			Message: fmt.Sprintf("the dry-run could not estimate the bytes (accuracy %s), so the limit cannot be checked; run it in the TUI", est.Accuracy)}
	}
	if est.Bytes > p.MaxBytes {
		return &api.Refusal{Code: api.RefuseOverLimit, StatementType: st, Bytes: est.Bytes, Limit: p.MaxBytes,
			Message: fmt.Sprintf("the query would process %s, over the limit of %s (agent_max_bytes); narrow it (partition filter, fewer columns), run it in the TUI, or raise agent_max_bytes in the config",
				config.FormatBytes(est.Bytes), config.FormatBytes(p.MaxBytes))}
	}
	if t := ConfirmThreshold(cfg, est.Capacity); t > 0 && est.Bytes > t {
		return &api.Refusal{Code: api.RefuseNeedsConfirmation, StatementType: st, Bytes: est.Bytes, Limit: t,
			Message: fmt.Sprintf("the query would process %s, over confirm_bytes (%s), which only the TUI can confirm; run it there or raise confirm_bytes in the config",
				config.FormatBytes(est.Bytes), config.FormatBytes(t))}
	}
	return nil
}

func (p Profile) where() string {
	if p.Source == "mcp" {
		return "the MCP server"
	}
	return "the command line"
}

// Error is a refusal as a Go error, so frontends can map it to an exit code
// or a tool error.
type Error struct{ Refusal api.Refusal }

func (e *Error) Error() string { return "refused: " + e.Refusal.Message }
