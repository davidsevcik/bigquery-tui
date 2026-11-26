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

const (
	DefaultRows = 100
	MaxRows     = 1000
	CellChars   = 200
)

type Profile struct {
	Source      string        `json:"source"`
	MaxBytes    int64         `json:"max_bytes"`
	Timeout     time.Duration `json:"timeout"`
	AllowWrites bool          `json:"allow_writes"`
}

func For(cfg *config.Config, source string) Profile {
	p := Profile{
		Source: source, MaxBytes: cfg.AgentMaxBytes, Timeout: cfg.AgentTimeout,
		AllowWrites: source == "cli" && cfg.CLIAllowWrites,
	}
	if p.MaxBytes <= 0 {
		p.MaxBytes = config.DefaultAgentMaxBytes
	}
	if p.Timeout <= 0 {
		p.Timeout = config.DefaultAgentTimeout
	}
	return p
}

func (p Profile) Lower(maxBytes int64, timeout time.Duration) Profile {
	if maxBytes > 0 && maxBytes < p.MaxBytes {
		p.MaxBytes = maxBytes
	}
	if timeout > 0 && timeout < p.Timeout {
		p.Timeout = timeout
	}
	return p
}

const AutoConfirmBytes int64 = 10 << 30

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
	case "PRECISE", "LOWER_BOUND", "UPPER_BOUND":
	default:
		return &api.Refusal{
			Code: api.RefuseUnknownEstimate, StatementType: st, Bytes: est.Bytes, Limit: p.MaxBytes,
			Message: fmt.Sprintf("the dry-run could not estimate the bytes (accuracy %s), so the limit cannot be checked; run it in the TUI", est.Accuracy),
		}
	}
	if est.Bytes > p.MaxBytes {
		return &api.Refusal{
			Code: api.RefuseOverLimit, StatementType: st, Bytes: est.Bytes, Limit: p.MaxBytes,
			Message: fmt.Sprintf("the query would process %s, over the limit of %s (agent_max_bytes); narrow it (partition filter, fewer columns), run it in the TUI, or raise agent_max_bytes in the config",
				config.FormatBytes(est.Bytes), config.FormatBytes(p.MaxBytes)),
		}
	}
	if t := ConfirmThreshold(cfg, est.Capacity); t > 0 && est.Bytes > t {
		return &api.Refusal{
			Code: api.RefuseNeedsConfirmation, StatementType: st, Bytes: est.Bytes, Limit: t,
			Message: fmt.Sprintf("the query would process %s, over confirm_bytes (%s), which only the TUI can confirm; run it there or raise confirm_bytes in the config",
				config.FormatBytes(est.Bytes), config.FormatBytes(t)),
		}
	}
	return nil
}

func (p Profile) where() string {
	if p.Source == "mcp" {
		return "the MCP server"
	}
	return "the command line"
}

type Error struct{ Refusal api.Refusal }

func (e *Error) Error() string { return "refused: " + e.Refusal.Message }
