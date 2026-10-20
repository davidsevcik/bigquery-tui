package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
)

func cfg() *config.Config {
	return &config.Config{AgentMaxBytes: 1 << 30, AgentTimeout: time.Minute,
		ConfirmBytes:   config.ByteLimit{Mode: config.ByteLimitAuto},
		MaxBytesBilled: config.ByteLimit{Mode: config.ByteLimitValue, Bytes: 1 << 40}}
}

func TestCheckEveryStatementAndAccuracy(t *testing.T) {
	c := cfg()
	writes := cfg()
	writes.CLIAllowWrites = true
	cases := []struct {
		name   string
		c      *config.Config
		source string
		est    api.Estimate
		want   string // refusal code, "" for allowed
	}{
		{"select", c, "mcp", api.Estimate{StatementType: "SELECT", Accuracy: "PRECISE", Bytes: 10}, ""},
		{"select lower bound under", c, "mcp", api.Estimate{StatementType: "SELECT", Accuracy: "LOWER_BOUND", Bytes: 1 << 29}, ""},
		{"select lower bound over", c, "mcp", api.Estimate{StatementType: "SELECT", Accuracy: "LOWER_BOUND", Bytes: 2 << 30}, api.RefuseOverLimit},
		{"select upper bound", c, "cli", api.Estimate{StatementType: "SELECT", Accuracy: "UPPER_BOUND", Bytes: 5}, ""},
		{"select unknown", c, "cli", api.Estimate{StatementType: "SELECT", Accuracy: "UNKNOWN", Bytes: 0}, api.RefuseUnknownEstimate},
		{"select odd accuracy", c, "cli", api.Estimate{StatementType: "SELECT", Accuracy: "SOMETHING_NEW"}, api.RefuseUnknownEstimate},
		{"select over limit", c, "cli", api.Estimate{StatementType: "SELECT", Accuracy: "PRECISE", Bytes: 1<<30 + 1}, api.RefuseOverLimit},
		{"select at limit", c, "cli", api.Estimate{StatementType: "SELECT", Accuracy: "PRECISE", Bytes: 1 << 30}, ""},
		{"no statement type", c, "cli", api.Estimate{Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
		{"no statement type, writes allowed", writes, "cli", api.Estimate{Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
		{"script", c, "mcp", api.Estimate{StatementType: "SCRIPT", Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
		{"export data", c, "cli", api.Estimate{StatementType: "EXPORT_DATA", Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
		{"call", c, "cli", api.Estimate{StatementType: "CALL", Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
		{"insert", c, "cli", api.Estimate{StatementType: "INSERT", Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
		{"drop", c, "cli", api.Estimate{StatementType: "DROP_TABLE", Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
		{"ctas", c, "cli", api.Estimate{StatementType: "CREATE_TABLE_AS_SELECT", Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
		{"insert, CLI writes allowed", writes, "cli", api.Estimate{StatementType: "INSERT", Accuracy: "PRECISE", Bytes: 5}, ""},
		{"script, CLI writes allowed", writes, "cli", api.Estimate{StatementType: "SCRIPT", Accuracy: "PRECISE"}, ""},
		{"insert over limit, CLI writes allowed", writes, "cli", api.Estimate{StatementType: "INSERT", Accuracy: "PRECISE", Bytes: 2 << 30}, api.RefuseOverLimit},
		{"insert, MCP ignores cli_allow_writes", writes, "mcp", api.Estimate{StatementType: "INSERT", Accuracy: "PRECISE"}, api.RefuseNotReadOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := For(tc.c, tc.source).Check(tc.c, tc.est)
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("refused: %+v", got)
			case tc.want != "" && got == nil:
				t.Fatalf("allowed, want %s", tc.want)
			case tc.want != "" && got.Code != tc.want:
				t.Fatalf("code %s, want %s (%s)", got.Code, tc.want, got.Message)
			}
		})
	}
}

func TestConfirmBytesRefusesWhatTheTUIWouldAsk(t *testing.T) {
	c := cfg()
	c.AgentMaxBytes = 1 << 40 // raised above confirm_bytes
	p := For(c, "cli")
	big := api.Estimate{StatementType: "SELECT", Accuracy: "PRECISE", Bytes: 20 << 30}
	if r := p.Check(c, big); r == nil || r.Code != api.RefuseNeedsConfirmation || r.Limit != AutoConfirmBytes {
		t.Fatalf("on-demand over auto confirm_bytes: %+v", r)
	}
	big.Capacity = true
	if r := p.Check(c, big); r != nil {
		t.Fatalf("auto confirm_bytes is off on capacity: %+v", r)
	}
	c.ConfirmBytes = config.ByteLimit{Mode: config.ByteLimitValue, Bytes: 15 << 30}
	if r := p.Check(c, big); r == nil || r.Code != api.RefuseNeedsConfirmation {
		t.Fatalf("an explicit confirm_bytes applies on capacity too: %+v", r)
	}
	c.ConfirmBytes = config.ByteLimit{Mode: config.ByteLimitOff}
	big.Capacity = false
	if r := p.Check(c, big); r != nil {
		t.Fatalf("confirm_bytes off: %+v", r)
	}
}

func TestLimitsCanOnlyBeLowered(t *testing.T) {
	c := cfg()
	p := For(c, "mcp")
	if p.MaxBytes != 1<<30 || p.Timeout != time.Minute || p.AllowWrites {
		t.Fatalf("%+v", p)
	}
	up := p.Lower(1<<40, time.Hour)
	if up != p {
		t.Fatalf("raising must be ignored: %+v", up)
	}
	down := p.Lower(1<<20, time.Second)
	if down.MaxBytes != 1<<20 || down.Timeout != time.Second {
		t.Fatalf("%+v", down)
	}
	if z := p.Lower(0, 0); z != p {
		t.Fatalf("zero keeps the limits: %+v", z)
	}
	if r := down.Check(c, api.Estimate{StatementType: "SELECT", Accuracy: "PRECISE", Bytes: 2 << 20}); r == nil || r.Limit != 1<<20 {
		t.Fatalf("lowered limit applies: %+v", r)
	}
}

func TestWritesAndDefaults(t *testing.T) {
	c := cfg()
	c.CLIAllowWrites = true
	if !For(c, "cli").AllowWrites || For(c, "mcp").AllowWrites {
		t.Fatal("cli_allow_writes is for the CLI only")
	}
	c.AgentMaxBytes, c.AgentTimeout = 0, 0
	if p := For(c, "cli"); p.MaxBytes != config.DefaultAgentMaxBytes || p.Timeout != config.DefaultAgentTimeout {
		t.Fatalf("defaults: %+v", p)
	}
}

func TestRefusalMessagesSayWhatToDo(t *testing.T) {
	c := cfg()
	r := For(c, "cli").Check(c, api.Estimate{StatementType: "DELETE", Accuracy: "PRECISE"})
	if !strings.Contains(r.Message, "cli_allow_writes") || !strings.Contains(r.Message, "DELETE") {
		t.Fatal(r.Message)
	}
	r = For(c, "mcp").Check(c, api.Estimate{StatementType: "DELETE", Accuracy: "PRECISE"})
	if strings.Contains(r.Message, "cli_allow_writes") {
		t.Fatal("the MCP server cannot be opened up: " + r.Message)
	}
	r = For(c, "mcp").Check(c, api.Estimate{StatementType: "SELECT", Accuracy: "PRECISE", Bytes: 3 << 30})
	if !strings.Contains(r.Message, "3GiB") || !strings.Contains(r.Message, "1GiB") {
		t.Fatal(r.Message)
	}
}
