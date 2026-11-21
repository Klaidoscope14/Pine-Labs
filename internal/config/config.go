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
	"os"
	"slices"
	"strings"

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

	// Hash is the SHA-256 of the canonical JSON form. Every run records it.
	Hash string `json:"-"`
}

type Deployment struct {
	Environment   string `json:"environment"`
	PrimaryRegion string `json:"primary_region"`
	DRRegion      string `json:"dr_region"`
}

type DataResidency struct {
	AllowedJurisdictions []string `json:"allowed_jurisdictions"`
}

type Endpoint struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	URL            string `json:"url"`
	Jurisdiction   string `json:"jurisdiction"`
	CredentialsRef string `json:"credentials_ref,omitempty"`
}

type LLM struct {
	Providers     []Provider `json:"providers"`
	FallbackOrder []string   `json:"fallback_order"`
}

type Provider struct {
	Name          string            `json:"name"`
	Endpoint      string            `json:"endpoint"`
	APIKeyRef     string            `json:"api_key_ref"`
	Models        map[string]string `json:"models"`
	PriceTableRef string            `json:"price_table_ref,omitempty"`
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
	Enabled  []string `json:"enabled"`
	RulesRef string   `json:"rules_ref"`
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
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
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

	endpoints := map[string]Endpoint{}
	for _, e := range c.Endpoints {
		if _, dup := endpoints[e.Name]; dup {
			add("endpoint %q is defined more than once", e.Name)
		}
		endpoints[e.Name] = e
		if !slices.Contains(c.DataResidency.AllowedJurisdictions, e.Jurisdiction) {
			add("endpoint %q is in jurisdiction %q, which is not in data_residency.allowed_jurisdictions %v",
				e.Name, e.Jurisdiction, c.DataResidency.AllowedJurisdictions)
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

	if c.Review.RequiredApprovers["protected"] < c.Review.RequiredApprovers["high"] {
		add("review.required_approvers.protected must be >= high")
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w:\n  - %s", ErrInvalid, strings.Join(problems, "\n  - "))
	}
	return nil
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
