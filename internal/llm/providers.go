package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/bedrock"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsbedrock "github.com/aws/aws-sdk-go-v2/service/bedrock"

	"aegis/internal/config"
)

// SDKProvider sends requests with the official Anthropic Go SDK, either to the
// first-party API or (kind=bedrock) to Amazon Bedrock.
type SDKProvider struct {
	name   string
	models map[string]string
	client anthropic.Client
}

func (p *SDKProvider) Name() string             { return p.name }
func (p *SDKProvider) Model(role string) string { return p.models[role] }
func (p *SDKProvider) Send(ctx context.Context, _ Call, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	return p.client.Messages.New(ctx, params)
}

// NewSDKProvider builds a provider from config. Bedrock credentials come from the
// named AWS profile in config (aws_profile); no credential is stored here.
func NewSDKProvider(ctx context.Context, cfg config.Provider, maxRetries int) (*SDKProvider, error) {
	opts := []option.RequestOption{option.WithMaxRetries(maxRetries)}
	switch cfg.Kind {
	case "bedrock":
		// Only the named profile is used: never environment keys or the default profile.
		// Environment credentials would silently override the profile, so refuse them.
		for _, v := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK"} {
			if os.Getenv(v) != "" {
				return nil, fmt.Errorf("provider %q: %s is set in the environment and would override aws_profile %q; unset it", cfg.Name, v, cfg.AWSProfile)
			}
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region),
			awsconfig.WithSharedConfigProfile(cfg.AWSProfile))
		if err != nil {
			return nil, fmt.Errorf("aws config: %w", err)
		}
		if cfg.VerifyProfileRouting {
			profiles := make([]string, 0, len(cfg.Models))
			for _, m := range cfg.Models {
				profiles = append(profiles, m)
			}
			if err := VerifyProfileRouting(ctx, awsbedrock.NewFromConfig(awsCfg), profiles, cfg.AllowedRegions); err != nil {
				return nil, fmt.Errorf("data residency: %w", err)
			}
		}
		opts = append(opts, bedrock.WithConfig(awsCfg))
	case "anthropic":
		key, err := config.ResolveSecret(cfg.APIKeyRef)
		if err != nil {
			return nil, err
		}
		opts = append(opts, option.WithAPIKey(key))
	default:
		return nil, fmt.Errorf("provider %q: kind %q is not an SDK provider", cfg.Name, cfg.Kind)
	}
	return &SDKProvider{name: cfg.Name, models: cfg.Models, client: anthropic.NewClient(opts...)}, nil
}

var safeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func cassettePath(dir string, c Call) string {
	return filepath.Join(dir, safeName.ReplaceAllString(c.Cassette, "_"),
		fmt.Sprintf("%s-a%d-%03d.json", safeName.ReplaceAllString(c.Role, "_"), c.Attempt, c.Seq))
}

// cassette is one recorded exchange. The request is kept (already redacted) so
// tests can prove what was sent; the response replays the call.
type cassette struct {
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}

// Recorder wraps a live provider and writes every successful exchange to disk.
type Recorder struct {
	Inner Provider
	Dir   string
}

func (r *Recorder) Name() string             { return r.Inner.Name() }
func (r *Recorder) Model(role string) string { return r.Inner.Model(role) }
func (r *Recorder) Send(ctx context.Context, call Call, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	msg, err := r.Inner.Send(ctx, call, params)
	if err != nil || call.Cassette == "" {
		return msg, err
	}
	req, _ := json.Marshal(params)
	data, err := json.MarshalIndent(cassette{Request: req, Response: json.RawMessage(msg.RawJSON())}, "", "  ")
	if err != nil {
		return msg, nil
	}
	path := cassettePath(r.Dir, call)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err == nil {
		tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
		if os.WriteFile(tmp, data, 0o640) == nil {
			_ = os.Rename(tmp, path)
		}
	}
	return msg, nil
}

// Replay serves recorded responses by (cassette, role, attempt, seq). It never
// touches the network. Sent requests are kept in memory for assertions.
type Replay struct {
	name   string
	Dir    string
	models map[string]string

	mu   sync.Mutex
	Sent []json.RawMessage
}

// NewReplay builds a replay provider.
func NewReplay(name, dir string, models map[string]string) *Replay {
	return &Replay{name: name, Dir: dir, models: models}
}

func (p *Replay) Name() string             { return p.name }
func (p *Replay) Model(role string) string { return p.models[role] }
func (p *Replay) Send(_ context.Context, call Call, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	req, _ := json.Marshal(params)
	p.mu.Lock()
	p.Sent = append(p.Sent, req)
	p.mu.Unlock()
	// Keep what would have been sent next to the cassette, so tests can prove
	// what left the machine (e.g. that a card number was redacted).
	sent := strings.TrimSuffix(cassettePath(p.Dir, call), ".json") + ".sent.json"
	_ = os.WriteFile(sent, req, 0o640)
	raw, err := os.ReadFile(cassettePath(p.Dir, call))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoCassette, strings.TrimPrefix(cassettePath(p.Dir, call), p.Dir+"/"))
	}
	if err != nil {
		return nil, err
	}
	var c cassette
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("cassette: %w", err)
	}
	var msg anthropic.Message
	if err := msg.UnmarshalJSON(c.Response); err != nil {
		return nil, fmt.Errorf("cassette response: %w", err)
	}
	return &msg, nil
}

// Build constructs the providers in fallback order from config.
func Build(ctx context.Context, cfg *config.Config) ([]Provider, error) {
	var out []Provider
	for _, name := range cfg.LLM.FallbackOrder {
		pc, _ := cfg.Provider(name)
		var p Provider
		switch pc.Kind {
		case "replay":
			p = NewReplay(pc.Name, cfg.Path(pc.CassetteDir), pc.Models)
		case "claude_code":
			ep, _ := cfg.Endpoint(pc.Endpoint)
			p = NewClaudeCode(pc.Name, pc.Command, ep.Jurisdiction, pc.Models)
			if pc.Record {
				p = &Recorder{Inner: p, Dir: cfg.Path(pc.CassetteDir)}
			}
		case "openai_compat":
			ep, _ := cfg.Endpoint(pc.Endpoint)
			key, err := config.ResolveSecret(pc.APIKeyRef)
			if err != nil {
				return nil, fmt.Errorf("provider %s: %w", pc.Name, err)
			}
			p = NewOpenAICompat(pc.Name, ep.URL, key, ep.Jurisdiction, pc.Models)
			if pc.Record {
				p = &Recorder{Inner: p, Dir: cfg.Path(pc.CassetteDir)}
			}
		default:
			sp, err := NewSDKProvider(ctx, pc, cfg.LLM.MaxRetries)
			if err != nil {
				return nil, err
			}
			p = sp
			if pc.Record {
				p = &Recorder{Inner: sp, Dir: cfg.Path(pc.CassetteDir)}
			}
		}
		out = append(out, p)
	}
	return out, nil
}
