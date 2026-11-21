// Package config loads, validates and guards the AegisSDLC configuration.
//
// Every region, endpoint, model, limit and threshold comes from here. Loading
// fails closed: a schema error, an untagged endpoint, or an endpoint outside the
// allowed jurisdictions stops the service from starting.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"aegis/schemas"
)

// Config is the typed view of config.schema.json.
type Config struct {
	Version       int                `json:"version"`
	Deployment    Deployment         `json:"deployment"`
	DataResidency DataResidency      `json:"data_residency"`
	Endpoints     []Endpoint         `json:"endpoints"`
	LLM           LLM                `json:"llm"`
	Workflow      Workflow           `json:"workflow"`
	Budget        Budget             `json:"budget"`
	Review        Review             `json:"review"`
	Scope         Scope              `json:"scope"`
	Sandbox       Sandbox            `json:"sandbox"`
	Tenants       []Tenant           `json:"tenants"`
	Tokens        Tokens             `json:"tokens"`
	Approval      Approval           `json:"approval"`
	API           API                `json:"api"`
	Gates         Gates              `json:"gates"`
	SLO           map[string]float64 `json:"slo,omitempty"`
	Load          Load               `json:"load,omitempty"`
	Policy        Policy             `json:"policy,omitempty"`
	Workspace     Workspace          `json:"workspace"`
	Temporal      Temporal           `json:"temporal"`
	Ledger        Ledger             `json:"ledger"`
	Repos         []Repo             `json:"repos"`
	Evals         Evals              `json:"evals"`
	Agents        Agents             `json:"agents"`

	// BaseDir is the directory of the config file; relative paths resolve against it.
	BaseDir string `json:"-"`

	// Hash is the SHA-256 of the canonical JSON form. Every run records it.
	Hash string `json:"-"`
}

type Deployment struct {
	Environment   string `json:"environment"`
	PrimaryRegion string `json:"primary_region"`
	DRRegion      string `json:"dr_region"`
}

type DataResidency struct {
	AllowedJurisdictions []string             `json:"allowed_jurisdictions"`
	Exceptions           []ResidencyException `json:"exceptions,omitempty"`
}

// ResidencyException lets one named endpoint sit outside the allowed
// jurisdictions for development only, until an expiry date.
type ResidencyException struct {
	Endpoint     string   `json:"endpoint"`
	Reason       string   `json:"reason"`
	Expires      string   `json:"expires"` // YYYY-MM-DD
	Environments []string `json:"environments"`
}

// now is replaceable in tests.
var now = time.Now

// maxExceptionDays caps how far ahead an exception may expire.
const maxExceptionDays = 90

type Endpoint struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	URL            string `json:"url"`
	Jurisdiction   string `json:"jurisdiction"`
	CredentialsRef string `json:"credentials_ref,omitempty"`
}

type LLM struct {
	Providers     []Provider              `json:"providers"`
	FallbackOrder []string                `json:"fallback_order"`
	Roles         map[string]RoleSettings `json:"roles"`
	MaxRetries    int                     `json:"max_retries"`
}

type Provider struct {
	Name                 string            `json:"name"`
	Kind                 string            `json:"kind"` // anthropic | bedrock | openai_compat | claude_code | replay
	Endpoint             string            `json:"endpoint"`
	APIKeyRef            string            `json:"api_key_ref"`
	Models               map[string]string `json:"models"`
	PriceTableRef        string            `json:"price_table_ref,omitempty"`
	Region               string            `json:"region,omitempty"`
	AWSProfile           string            `json:"aws_profile,omitempty"`
	CassetteDir          string            `json:"cassette_dir,omitempty"`
	Record               bool              `json:"record,omitempty"`
	VerifyProfileRouting bool              `json:"verify_profile_routing,omitempty"`
	AllowedRegions       []string          `json:"allowed_regions,omitempty"`
	// Command is the Claude Code CLI to run (kind claude_code; default "claude").
	Command string `json:"command,omitempty"`
}

// RoleSettings tunes one agent role.
type RoleSettings struct {
	Effort    string `json:"effort"`
	MaxTokens int    `json:"max_tokens"`
	// RoutineEffort (developer only) is used for routine tool turns; the first turn
	// and any turn after a failed tool call, check or refused finish use Effort.
	RoutineEffort string `json:"routine_effort,omitempty"`
}

// Agents configures how agents run.
type Agents struct {
	Mode         string `json:"mode"` // stub | llm
	MaxToolCalls int    `json:"max_tool_calls"`
	MaxReadBytes int    `json:"max_read_bytes"`
	MaxRunTests  int    `json:"max_run_tests"`
	// MaxFinishRefusals is how many times finish is refused while the self-check
	// (diff budget, lint, tests) fails; 0 turns the self-check off.
	MaxFinishRefusals int    `json:"max_finish_refusals"`
	MaxContextBytes   int    `json:"max_context_bytes"`
	PromptsDir        string `json:"prompts_dir"`
	ImportGraphScript string `json:"import_graph_script"`
}

type Workflow struct {
	MaxCIRounds          int `json:"max_ci_rounds"`
	MaxSchemaRepairs     int `json:"max_schema_repairs"`
	MaxArbitrationRounds int `json:"max_arbitration_rounds"`
}

type Budget struct {
	PerRunTokens      int     `json:"per_run_tokens"`
	PerRunUSD         float64 `json:"per_run_usd"`
	PerTenantDailyUSD float64 `json:"per_tenant_daily_usd"`
}

type Review struct {
	WIPPerReviewer    int            `json:"wip_per_reviewer"`
	RequiredApprovers map[string]int `json:"required_approvers"`
}

type Scope struct {
	ProtectedPaths  []string   `json:"protected_paths"`
	DiffBudget      DiffBudget `json:"diff_budget"`
	LeaseTTLSeconds int        `json:"lease_ttl_seconds"`
}

type DiffBudget struct {
	Files int `json:"files"`
	Lines int `json:"lines"`
}

type Sandbox struct {
	Image          string  `json:"image"`
	CPUs           float64 `json:"cpus"`
	MemoryMB       int     `json:"memory_mb"`
	PIDs           int     `json:"pids"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	WarmPoolSize   int     `json:"warm_pool_size"`
	Network        string  `json:"network"`
	MaxOutputBytes int64   `json:"max_output_bytes"`
	// MaxConcurrent caps sandboxes running at once on this host (queue, never overload).
	MaxConcurrent int `json:"max_concurrent"`
	// HostReserveMB is memory kept for the platform, worker and OS; the worker refuses
	// to start if max_concurrent × memory_mb + host_reserve_mb exceeds the host's memory.
	HostReserveMB int `json:"host_reserve_mb"`
	// Runtime is the OCI runtime for sandboxes: "" (Docker default) or "runsc" (gVisor).
	Runtime string `json:"runtime,omitempty"`
}

type Tenant struct {
	ID            string  `json:"id"`
	Concurrency   int     `json:"concurrency"`
	QueuePriority string  `json:"queue_priority"`
	DailyUSD      float64 `json:"daily_usd"`
}

type Tokens struct {
	CapabilityTTLSeconds int `json:"capability_ttl_seconds"`
}

type Approval struct {
	ExpirySeconds int `json:"expiry_seconds"`
}

type API struct {
	Listen string `json:"listen"`
}

type Gates struct {
	Enabled         []string `json:"enabled"`
	Tier1           []string `json:"tier1"`
	Security        []string `json:"security"`
	Tier2           []string `json:"tier2"`
	Parallelism     int      `json:"parallelism"`
	SemgrepRulesDir string   `json:"semgrep_rules_dir"`
	RulesRef        string   `json:"rules_ref"`
}

type Workspace struct {
	MirrorsDir   string `json:"mirrors_dir"`
	RunsDir      string `json:"runs_dir"`
	ArtifactsDir string `json:"artifacts_dir"`
	AuthorName   string `json:"commit_author_name"`
	AuthorEmail  string `json:"commit_author_email"`
}

type Temporal struct {
	Endpoint  string `json:"endpoint"`
	Namespace string `json:"namespace"`
	TaskQueue string `json:"task_queue"`
	// MaxConcurrentActivities bounds how many activities one worker runs at once.
	MaxConcurrentActivities int `json:"max_concurrent_activities"`
	// HeartbeatSeconds bounds how long a dead worker's activity goes unnoticed.
	HeartbeatSeconds int `json:"heartbeat_seconds"`
}

type Ledger struct {
	AdminEndpoint  string `json:"admin_endpoint"`
	AppEndpoint    string `json:"app_endpoint"`
	AppRole        string `json:"app_role"`
	AppPasswordRef string `json:"app_password_ref"`
}

type Repo struct {
	Name            string   `json:"name"`
	Endpoint        string   `json:"endpoint"`
	Path            string   `json:"path"`
	DefaultBranch   string   `json:"default_branch"`
	SourceDirs      []string `json:"source_dirs"`
	DependencyFiles []string `json:"dependency_files"`
	OpenAPIExport   []string `json:"openapi_export"`
}

type Evals struct {
	HiddenTestsDir string `json:"hidden_tests_dir"`
	ReplayDir      string `json:"replay_dir"`
	ScenariosDir   string `json:"scenarios_dir"`
}

type Load struct {
	ConcurrentRuns int `json:"concurrent_runs,omitempty"`
}

type Policy struct {
	BundleRef string `json:"bundle_ref,omitempty"`
}

// ErrInvalid wraps every validation failure so callers can test for it.
var ErrInvalid = errors.New("invalid configuration")

var compiled *jsonschema.Schema

func schema() (*jsonschema.Schema, error) {
	if compiled != nil {
		return compiled, nil
	}
	raw, err := schemas.FS.ReadFile("config.schema.json")
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("config.schema.json", doc); err != nil {
		return nil, err
	}
	s, err := c.Compile("config.schema.json")
	if err != nil {
		return nil, err
	}
	compiled = s
	return s, nil
}

// LoadFile reads, validates and guards a YAML config file.
func LoadFile(path string) (*Config, error) {
	raw, err := resolveExtends(path, 0)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	cfg.BaseDir = abs
	return cfg, nil
}

// resolveExtends loads a config file and, if it has a top-level `extends: <file>`,
// deep-merges it over that base (maps merge, lists and scalars replace). The merged
// document is what gets validated, so an overlay can never skip the guards.
func resolveExtends(path string, depth int) ([]byte, error) {
	if depth > 4 {
		return nil, fmt.Errorf("%w: extends chain too deep at %s", ErrInvalid, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: yaml: %v", ErrInvalid, err)
	}
	base, ok := doc["extends"].(string)
	if !ok {
		return raw, nil
	}
	delete(doc, "extends")
	baseRaw, err := resolveExtends(filepath.Join(filepath.Dir(path), base), depth+1)
	if err != nil {
		return nil, err
	}
	var baseDoc map[string]any
	if err := yaml.Unmarshal(baseRaw, &baseDoc); err != nil {
		return nil, fmt.Errorf("%w: yaml: %v", ErrInvalid, err)
	}
	return yaml.Marshal(merge(baseDoc, doc))
}

func merge(base, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if bm, ok := out[k].(map[string]any); ok {
			if om, ok := v.(map[string]any); ok {
				out[k] = merge(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// Parse validates YAML bytes against the schema, then runs the semantic guards.
func Parse(raw []byte) (*Config, error) {
	var generic any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("%w: yaml: %v", ErrInvalid, err)
	}
	// Round-trip through JSON so the validator sees JSON types only.
	canonical, err := json.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("%w: yaml is not JSON-compatible: %v", ErrInvalid, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(canonical))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	s, err := schema()
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	if err := s.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: schema: %v", ErrInvalid, err)
	}

	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrInvalid, err)
	}
	if err := cfg.guard(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	cfg.Hash = hex.EncodeToString(sum[:])
	return &cfg, nil
}

// guard enforces rules a JSON Schema cannot express.
func (c *Config) guard() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if c.Deployment.PrimaryRegion == c.Deployment.DRRegion {
		add("deployment.dr_region must differ from deployment.primary_region")
	}

	exceptions := map[string]ResidencyException{}
	today := now().UTC().Truncate(24 * time.Hour)
	for _, ex := range c.DataResidency.Exceptions {
		exp, err := time.Parse("2006-01-02", ex.Expires)
		switch {
		case err != nil:
			add("residency exception for %q: bad expires date %q", ex.Endpoint, ex.Expires)
		case c.Deployment.Environment == "production":
			add("residency exception for %q: exceptions are not allowed in production", ex.Endpoint)
		case !slices.Contains(ex.Environments, c.Deployment.Environment):
			// Not active in this environment; the endpoint must then be in-jurisdiction.
		case exp.Before(today):
			add("residency exception for %q expired on %s", ex.Endpoint, ex.Expires)
		case exp.After(today.AddDate(0, 0, maxExceptionDays)):
			add("residency exception for %q expires more than %d days ahead (%s)", ex.Endpoint, maxExceptionDays, ex.Expires)
		default:
			exceptions[ex.Endpoint] = ex
		}
	}
	endpoints := map[string]Endpoint{}
	for _, e := range c.Endpoints {
		if _, dup := endpoints[e.Name]; dup {
			add("endpoint %q is defined more than once", e.Name)
		}
		endpoints[e.Name] = e
		if !slices.Contains(c.DataResidency.AllowedJurisdictions, e.Jurisdiction) {
			if _, ok := exceptions[e.Name]; !ok {
				add("endpoint %q is in jurisdiction %q, which is not in data_residency.allowed_jurisdictions %v",
					e.Name, e.Jurisdiction, c.DataResidency.AllowedJurisdictions)
			}
		}
	}
	for _, ex := range c.DataResidency.Exceptions {
		if _, ok := endpoints[ex.Endpoint]; !ok {
			add("residency exception names unknown endpoint %q", ex.Endpoint)
		}
	}

	providers := map[string]bool{}
	for _, p := range c.LLM.Providers {
		providers[p.Name] = true
		e, ok := endpoints[p.Endpoint]
		switch {
		case !ok:
			add("llm provider %q references unknown endpoint %q", p.Name, p.Endpoint)
		case e.Kind != "llm":
			add("llm provider %q references endpoint %q of kind %q, want llm", p.Name, p.Endpoint, e.Kind)
		}
	}
	for _, p := range c.LLM.Providers {
		switch p.Kind {
		case "bedrock":
			if p.Region == "" {
				add("llm provider %q (bedrock) needs region", p.Name)
			}
			if p.AWSProfile == "" || p.AWSProfile == "default" {
				add("llm provider %q (bedrock) needs an explicit aws_profile (not \"default\") so the paying account is chosen deliberately", p.Name)
			}
			if p.VerifyProfileRouting && len(p.AllowedRegions) == 0 {
				add("llm provider %q verifies profile routing but lists no allowed_regions", p.Name)
			}
		case "claude_code":
			// Uses a personal Claude subscription through the local CLI: benchmarking only.
			if c.Deployment.Environment == "production" {
				add("llm provider %q (claude_code) is for local benchmarking and is not allowed in production", p.Name)
			}
		case "replay":
			if p.CassetteDir == "" {
				add("llm provider %q (replay) needs cassette_dir", p.Name)
			}
		}
		if p.Record && p.CassetteDir == "" {
			add("llm provider %q records but has no cassette_dir", p.Name)
		}
		if p.Record && c.Deployment.Environment == "production" {
			add("llm provider %q: recording responses is not allowed in production", p.Name)
		}
	}
	for _, name := range c.LLM.FallbackOrder {
		if !providers[name] {
			add("llm.fallback_order lists unknown provider %q", name)
		}
	}

	tenants := map[string]bool{}
	for _, t := range c.Tenants {
		if tenants[t.ID] {
			add("tenant %q is defined more than once", t.ID)
		}
		tenants[t.ID] = true
	}

	wantKind := func(field, name, kind string) {
		e, ok := endpoints[name]
		switch {
		case !ok:
			add("%s references unknown endpoint %q", field, name)
		case e.Kind != kind:
			add("%s references endpoint %q of kind %q, want %s", field, name, e.Kind, kind)
		}
	}
	wantKind("temporal.endpoint", c.Temporal.Endpoint, "workflow")
	wantKind("ledger.admin_endpoint", c.Ledger.AdminEndpoint, "database")
	wantKind("ledger.app_endpoint", c.Ledger.AppEndpoint, "database")
	repos := map[string]bool{}
	for _, r := range c.Repos {
		if repos[r.Name] {
			add("repo %q is defined more than once", r.Name)
		}
		repos[r.Name] = true
		wantKind(fmt.Sprintf("repo %q endpoint", r.Name), r.Endpoint, "code_host")
	}
	for tier, gates := range map[string][]string{"tier1": c.Gates.Tier1, "security": c.Gates.Security, "tier2": c.Gates.Tier2} {
		for _, g := range gates {
			if !slices.Contains(c.Gates.Enabled, g) {
				add("gates.%s lists %q, which is not in gates.enabled", tier, g)
			}
		}
	}

	if c.Review.RequiredApprovers["protected"] < c.Review.RequiredApprovers["high"] {
		add("review.required_approvers.protected must be >= high")
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w:\n  - %s", ErrInvalid, strings.Join(problems, "\n  - "))
	}
	return nil
}

// Provider returns the named LLM provider.
func (c *Config) Provider(name string) (Provider, bool) {
	for _, p := range c.LLM.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// Endpoint returns the named endpoint.
func (c *Config) Endpoint(name string) (Endpoint, bool) {
	for _, e := range c.Endpoints {
		if e.Name == name {
			return e, true
		}
	}
	return Endpoint{}, false
}

// ActiveExceptions lists the residency exceptions in force (printed at startup).
func (c *Config) ActiveExceptions() []ResidencyException {
	var out []ResidencyException
	for _, ex := range c.DataResidency.Exceptions {
		if slices.Contains(ex.Environments, c.Deployment.Environment) {
			out = append(out, ex)
		}
	}
	return out
}

// Path resolves a config-relative path to an absolute one.
func (c *Config) Path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.BaseDir, p)
}

// Repo returns the named repository.
func (c *Config) Repo(name string) (Repo, bool) {
	for _, r := range c.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return Repo{}, false
}

// RepoRemote returns the git remote URL for a repo, built from its code_host endpoint.
// Relative file:// URLs resolve against the config directory.
func (c *Config) RepoRemote(r Repo) (string, error) {
	e, ok := c.Endpoint(r.Endpoint)
	if !ok {
		return "", fmt.Errorf("repo %q: unknown endpoint %q", r.Name, r.Endpoint)
	}
	base := e.URL
	if rest, ok := strings.CutPrefix(base, "file://"); ok {
		return filepath.Join(c.Path(rest), r.Path), nil
	}
	return strings.TrimRight(base, "/") + "/" + r.Path, nil
}

// ErrSecretUnavailable is returned when a secret reference cannot be resolved.
var ErrSecretUnavailable = errors.New("secret unavailable")

// ResolveSecret turns an env:/vault: reference into its value at runtime.
// Values are never logged or stored.
func ResolveSecret(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "env:"):
		name := strings.TrimPrefix(ref, "env:")
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			return "", fmt.Errorf("%w: environment variable %s is not set", ErrSecretUnavailable, name)
		}
		return v, nil
	case strings.HasPrefix(ref, "vault:"):
		return "", fmt.Errorf("%w: vault references are not supported yet (%s)", ErrSecretUnavailable, ref)
	default:
		return "", fmt.Errorf("%w: unsupported reference %q", ErrSecretUnavailable, ref)
	}
}

// DSN builds a Postgres connection string for a database endpoint, with the
// password taken from the endpoint's credentials_ref.
func (c *Config) DSN(endpoint string) (string, error) {
	e, ok := c.Endpoint(endpoint)
	if !ok || e.Kind != "database" {
		return "", fmt.Errorf("no database endpoint %q", endpoint)
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return "", fmt.Errorf("endpoint %q: %w", endpoint, err)
	}
	if e.CredentialsRef != "" {
		pw, err := ResolveSecret(e.CredentialsRef)
		if err != nil {
			return "", err
		}
		u.User = url.UserPassword(u.User.Username(), pw)
	}
	q := u.Query()
	if q.Get("sslmode") == "" && c.Deployment.Environment == "local" {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
