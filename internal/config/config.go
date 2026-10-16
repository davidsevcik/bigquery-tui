// Package config loads, validates and describes bqt's single TOML
// configuration file (ticket 05, spec section 4).
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Defaults.
const (
	DefaultSyncTTL              = 24 * time.Hour
	DefaultMaxBytesBilled int64 = 1 << 40
	DefaultAgentMaxBytes  int64 = 1 << 30
	DefaultAgentTimeout         = 60 * time.Second
	DefaultMaxConcurrent        = 4
)

// Editing styles of the TUI's query editor ([tui] editing).
const (
	EditingVim   = "vim"
	EditingPlain = "plain"
)

// Editing returns the editor's editing style: vim (the default) or plain.
func (c *Config) Editing() string {
	if s, _ := c.TUI["editing"].(string); s == EditingPlain {
		return EditingPlain
	}
	return EditingVim
}

// Config is the effective, validated configuration.
type Config struct {
	ExecutionProject     string
	Location             string
	Projects             []string
	Ignore               []string
	DataDir              string
	FavoritesDir         string
	SyncTTL              time.Duration
	ConfirmBytes         ByteLimit
	MaxBytesBilled       ByteLimit
	MaxConcurrentQueries int
	HistoryRetentionDays int
	AgentMaxBytes        int64
	AgentTimeout         time.Duration
	CLIAllowWrites       bool
	TUI                  map[string]any

	// Path is the file the configuration was read from.
	Path string

	matcher *Matcher
}

// Matcher returns the compiled ignore matcher.
func (c *Config) Matcher() *Matcher { return c.matcher }

// Filter is the part of the configuration that decides what the catalog
// contains. Its hash decides "config changed, sync needed".
type Filter struct {
	Projects []string `json:"projects"`
	Ignore   []string `json:"ignore"`
}

// Filter returns the effective project/ignore filter.
func (c *Config) Filter() Filter {
	ps := append([]string(nil), c.Projects...)
	sort.Strings(ps)
	ig := []string{}
	for _, p := range c.Ignore {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		ig = append(ig, p)
	}
	return Filter{Projects: ps, Ignore: ig}
}

// Hash is a stable hash of the effective filter.
func (f Filter) Hash() string {
	b, _ := json.Marshal(f)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Error is one configuration problem.
type Error struct {
	Key  string
	Line int // 0 when unknown
	Msg  string
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Line > 0 {
		fmt.Fprintf(&b, "line %d: ", e.Line)
	}
	if e.Key != "" {
		fmt.Fprintf(&b, "%s: ", e.Key)
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Errors is a list of configuration problems.
type Errors []*Error

func (es Errors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// ErrNoConfig is returned when no configuration file exists.
type ErrNoConfig struct{ Path string }

func (e *ErrNoConfig) Error() string {
	return fmt.Sprintf("no config file at %s (run `bqt init` to create one)", e.Path)
}

// LoadOptions controls Load.
type LoadOptions struct {
	// Path overrides the config location (--config). Empty falls back to
	// BQT_CONFIG, then the default path.
	Path string
	// Getenv defaults to os.Getenv.
	Getenv func(string) string
	// Flags are command-line overrides keyed by config key
	// (for example "execution_project"). They win over the environment.
	Flags map[string]string
}

func (o LoadOptions) getenv(k string) string {
	if o.Getenv != nil {
		return o.Getenv(k)
	}
	return os.Getenv(k)
}

// DefaultPath returns $XDG_CONFIG_HOME/bqt/config.toml or
// ~/.config/bqt/config.toml.
func DefaultPath(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "bqt", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "bqt", "config.toml")
}

// ResolvePath applies the --config / BQT_CONFIG / default order.
func ResolvePath(opts LoadOptions) string {
	if opts.Path != "" {
		return expandPath(opts.Path, opts.getenv)
	}
	if p := opts.getenv("BQT_CONFIG"); p != "" {
		return expandPath(p, opts.getenv)
	}
	return DefaultPath(opts.getenv)
}

type fileConfig struct {
	ExecutionProject     *string        `toml:"execution_project"`
	Location             *string        `toml:"location"`
	Projects             []string       `toml:"projects"`
	Ignore               []string       `toml:"ignore"`
	DataDir              *string        `toml:"data_dir"`
	FavoritesDir         *string        `toml:"favorites_dir"`
	SyncTTL              *string        `toml:"sync_ttl"`
	ConfirmBytes         any            `toml:"confirm_bytes"`
	MaxBytesBilled       any            `toml:"max_bytes_billed"`
	MaxConcurrentQueries *int64         `toml:"max_concurrent_queries"`
	HistoryRetentionDays *int64         `toml:"history_retention_days"`
	AgentMaxBytes        any            `toml:"agent_max_bytes"`
	AgentTimeout         *string        `toml:"agent_timeout"`
	CLIAllowWrites       *bool          `toml:"cli_allow_writes"`
	TUI                  map[string]any `toml:"tui"`
	GUI                  map[string]any `toml:"gui"`
}

// envKeys lists the keys that can be set through BQT_<UPPERCASE KEY>.
var envKeys = []string{
	"execution_project", "location", "projects", "data_dir", "favorites_dir",
	"sync_ttl", "confirm_bytes", "max_bytes_billed", "max_concurrent_queries",
	"history_retention_days", "agent_max_bytes", "agent_timeout", "cli_allow_writes",
}

// Load reads the config file, applies environment and flag overrides, and
// validates the result. Problems are returned as Errors; a missing file as
// *ErrNoConfig.
func Load(opts LoadOptions) (*Config, error) {
	path := ResolvePath(opts)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &ErrNoConfig{Path: path}
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw, path, opts)
}

// Parse is Load for in-memory content.
func Parse(raw []byte, path string, opts LoadOptions) (*Config, error) {
	var fc fileConfig
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var errs Errors
	if err := dec.Decode(&fc); err != nil {
		var strict *toml.StrictMissingError
		var de *toml.DecodeError
		switch {
		case errors.As(err, &strict):
			for _, e := range strict.Errors {
				row, _ := e.Position()
				errs = append(errs, &Error{Key: strings.Join(e.Key(), "."), Line: row, Msg: "unknown key"})
			}
		case errors.As(err, &de):
			row, _ := de.Position()
			errs = append(errs, &Error{Line: row, Msg: de.Error()})
		default:
			errs = append(errs, &Error{Msg: err.Error()})
		}
		if len(errs) > 0 {
			return nil, errs
		}
	}

	b := &builder{raw: string(raw), getenv: opts.getenv}
	values := map[string]layered{}
	put := func(key string, v any, src string) { values[key] = layered{v: v, src: src} }

	// file layer
	if fc.ExecutionProject != nil {
		put("execution_project", *fc.ExecutionProject, "file")
	}
	if fc.Location != nil {
		put("location", *fc.Location, "file")
	}
	if fc.Projects != nil {
		put("projects", fc.Projects, "file")
	}
	if fc.DataDir != nil {
		put("data_dir", *fc.DataDir, "file")
	}
	if fc.FavoritesDir != nil {
		put("favorites_dir", *fc.FavoritesDir, "file")
	}
	if fc.SyncTTL != nil {
		put("sync_ttl", *fc.SyncTTL, "file")
	}
	if fc.ConfirmBytes != nil {
		put("confirm_bytes", fc.ConfirmBytes, "file")
	}
	if fc.MaxBytesBilled != nil {
		put("max_bytes_billed", fc.MaxBytesBilled, "file")
	}
	if fc.MaxConcurrentQueries != nil {
		put("max_concurrent_queries", *fc.MaxConcurrentQueries, "file")
	}
	if fc.HistoryRetentionDays != nil {
		put("history_retention_days", *fc.HistoryRetentionDays, "file")
	}
	if fc.AgentMaxBytes != nil {
		put("agent_max_bytes", fc.AgentMaxBytes, "file")
	}
	if fc.AgentTimeout != nil {
		put("agent_timeout", *fc.AgentTimeout, "file")
	}
	if fc.CLIAllowWrites != nil {
		put("cli_allow_writes", *fc.CLIAllowWrites, "file")
	}
	// environment layer
	for _, k := range envKeys {
		if v := opts.getenv("BQT_" + strings.ToUpper(k)); v != "" {
			put(k, v, "env BQT_"+strings.ToUpper(k))
		}
	}
	// flag layer
	for k, v := range opts.Flags {
		if v != "" {
			put(k, v, "flag --"+strings.ReplaceAll(k, "_", "-"))
		}
	}

	c := &Config{Path: path, TUI: fc.TUI, Ignore: fc.Ignore}
	if c.TUI == nil {
		c.TUI = map[string]any{}
	}
	b.build(c, values, &errs)
	for k, v := range c.TUI {
		switch k {
		case "editing":
			if s, ok := v.(string); !ok || (s != EditingVim && s != EditingPlain) {
				errs = append(errs, &Error{Key: "tui.editing", Line: b.lineOfKey("editing"), Msg: fmt.Sprintf(`must be "%s" or "%s"`, EditingVim, EditingPlain)})
			}
		default:
			errs = append(errs, &Error{Key: "tui." + k, Line: b.lineOfKey(k), Msg: "unknown key"})
		}
	}

	m, perrs := NewMatcher(c.Ignore)
	for _, pe := range perrs {
		errs = append(errs, &Error{Key: fmt.Sprintf("ignore[%d]", pe.Index), Line: b.lineOfValue(pe.Pattern), Msg: fmt.Sprintf("invalid pattern %q: %s", pe.Pattern, pe.Reason)})
	}
	c.matcher = m

	if len(errs) > 0 {
		return nil, errs
	}
	return c, nil
}

type layered struct {
	v   any
	src string
}

type builder struct {
	raw    string
	getenv func(string) string
}

func (b *builder) lineOfKey(key string) int {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	loc := re.FindStringIndex(b.raw)
	if loc == nil {
		return 0
	}
	return strings.Count(b.raw[:loc[0]], "\n") + 1
}

func (b *builder) lineOfValue(val string) int {
	idx := strings.Index(b.raw, val)
	if idx < 0 {
		return 0
	}
	return strings.Count(b.raw[:idx], "\n") + 1
}

var projectRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\-]*$`)

func (b *builder) build(c *Config, v map[string]layered, errs *Errors) {
	fail := func(key, src, msg string) {
		e := &Error{Key: key, Msg: msg}
		if src == "file" {
			e.Line = b.lineOfKey(key)
		} else if src != "" {
			e.Msg = fmt.Sprintf("%s (from %s)", msg, src)
		}
		*errs = append(*errs, e)
	}
	str := func(key string) (string, bool) {
		l, ok := v[key]
		if !ok {
			return "", false
		}
		s, isStr := l.v.(string)
		if !isStr {
			fail(key, l.src, fmt.Sprintf("expected a string, got %T", l.v))
			return "", false
		}
		return strings.TrimSpace(s), true
	}

	// execution_project (required)
	if s, ok := str("execution_project"); ok {
		if !projectRe.MatchString(s) {
			fail("execution_project", v["execution_project"].src, fmt.Sprintf("%q is not a valid project ID", s))
		}
		c.ExecutionProject = s
	} else if _, present := v["execution_project"]; !present {
		*errs = append(*errs, &Error{Key: "execution_project", Msg: "required (the project query and sync jobs run in); set it in the config file, BQT_EXECUTION_PROJECT or --execution-project"})
	}

	if s, ok := str("location"); ok {
		if s == "" {
			fail("location", v["location"].src, "must not be empty (omit the key to let BigQuery infer it)")
		}
		c.Location = s
	}

	if l, ok := v["projects"]; ok {
		var list []string
		switch x := l.v.(type) {
		case []string:
			list = x
		case string:
			list = append(list, strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' })...)
		}
		seen := map[string]bool{}
		for _, p := range list {
			p = strings.TrimSpace(p)
			if !projectRe.MatchString(p) {
				fail("projects", l.src, fmt.Sprintf("%q is not a valid project ID", p))
				continue
			}
			if seen[p] {
				fail("projects", l.src, fmt.Sprintf("duplicate project %q", p))
				continue
			}
			seen[p] = true
			c.Projects = append(c.Projects, p)
		}
	}

	// paths
	dataDir := ""
	if s, ok := str("data_dir"); ok {
		dataDir = expandPath(s, b.getenv)
	} else {
		dataDir = defaultDataDir(b.getenv)
	}
	c.DataDir = dataDir
	if s, ok := str("favorites_dir"); ok {
		c.FavoritesDir = expandPath(s, b.getenv)
	} else {
		c.FavoritesDir = filepath.Join(dataDir, "favorites")
	}

	// durations
	c.SyncTTL = DefaultSyncTTL
	if s, ok := str("sync_ttl"); ok {
		d, err := ParseDuration(s)
		switch {
		case err != nil:
			fail("sync_ttl", v["sync_ttl"].src, err.Error())
		case d <= 0:
			fail("sync_ttl", v["sync_ttl"].src, "must be positive")
		default:
			c.SyncTTL = d
		}
	}
	c.AgentTimeout = DefaultAgentTimeout
	if s, ok := str("agent_timeout"); ok {
		d, err := ParseDuration(s)
		switch {
		case err != nil:
			fail("agent_timeout", v["agent_timeout"].src, err.Error())
		case d <= 0:
			fail("agent_timeout", v["agent_timeout"].src, "must be positive")
		default:
			c.AgentTimeout = d
		}
	}

	// byte limits
	c.ConfirmBytes = ByteLimit{Mode: ByteLimitAuto}
	if l, ok := v["confirm_bytes"]; ok {
		if bl, err := parseByteLimit(l.v, true); err != nil {
			fail("confirm_bytes", l.src, err.Error())
		} else {
			c.ConfirmBytes = bl
		}
	}
	c.MaxBytesBilled = ByteLimit{Mode: ByteLimitValue, Bytes: DefaultMaxBytesBilled}
	if l, ok := v["max_bytes_billed"]; ok {
		if bl, err := parseByteLimit(l.v, false); err != nil {
			fail("max_bytes_billed", l.src, err.Error())
		} else {
			c.MaxBytesBilled = bl
		}
	}
	c.AgentMaxBytes = DefaultAgentMaxBytes
	if l, ok := v["agent_max_bytes"]; ok {
		n, err := ParseBytes(l.v)
		switch {
		case err != nil:
			fail("agent_max_bytes", l.src, err.Error())
		case n <= 0:
			fail("agent_max_bytes", l.src, "must be positive")
		default:
			c.AgentMaxBytes = n
		}
	}

	// integers
	c.MaxConcurrentQueries = DefaultMaxConcurrent
	if l, ok := v["max_concurrent_queries"]; ok {
		n, err := asInt(l.v)
		switch {
		case err != nil:
			fail("max_concurrent_queries", l.src, err.Error())
		case n < 1 || n > 16:
			fail("max_concurrent_queries", l.src, "must be between 1 and 16")
		default:
			c.MaxConcurrentQueries = int(n)
		}
	}
	if l, ok := v["history_retention_days"]; ok {
		n, err := asInt(l.v)
		switch {
		case err != nil:
			fail("history_retention_days", l.src, err.Error())
		case n < 0:
			fail("history_retention_days", l.src, "must not be negative")
		case n > 36500:
			fail("history_retention_days", l.src, "must be at most 36500 (100 years)")
		default:
			c.HistoryRetentionDays = int(n)
		}
	}

	if l, ok := v["cli_allow_writes"]; ok {
		switch x := l.v.(type) {
		case bool:
			c.CLIAllowWrites = x
		case string:
			bv, err := strconv.ParseBool(x)
			if err != nil {
				fail("cli_allow_writes", l.src, "expected true or false")
			}
			c.CLIAllowWrites = bv
		default:
			fail("cli_allow_writes", l.src, "expected true or false")
		}
	}
}

func parseByteLimit(v any, allowAuto bool) (ByteLimit, error) {
	if s, ok := v.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "off":
			return ByteLimit{Mode: ByteLimitOff}, nil
		case "auto":
			if allowAuto {
				return ByteLimit{Mode: ByteLimitAuto}, nil
			}
			return ByteLimit{}, fmt.Errorf("\"auto\" is not valid here (use a size or \"off\")")
		}
	}
	n, err := ParseBytes(v)
	if err != nil {
		return ByteLimit{}, err
	}
	if n <= 0 {
		return ByteLimit{}, fmt.Errorf("must be positive (use \"off\" to disable)")
	}
	return ByteLimit{Mode: ByteLimitValue, Bytes: n}, nil
}

func asInt(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("expected an integer, got %q", x)
		}
		return n, nil
	}
	return 0, fmt.Errorf("expected an integer, got %T", v)
}

func defaultDataDir(getenv func(string) string) string {
	if x := getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "bqt")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "bqt")
}

// expandPath expands ~ and $VARS in path values.
func expandPath(p string, getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	p = os.Expand(p, getenv)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}
