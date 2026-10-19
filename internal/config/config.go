package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

type Protocol string

const (
	ProtocolChat      Protocol = "chat"
	ProtocolResponses Protocol = "responses"
	ProtocolAnthropic Protocol = "anthropic"
)

type Project struct {
	Name               string   `json:"name"`
	Upstream           string   `json:"upstream"`
	APIKeyEnv          string   `json:"api_key_env,omitempty"`
	AnthropicAPIKeyEnv string   `json:"anthropic_api_key_env,omitempty"`
	Native             []string `json:"native,omitempty"`
	HealthPath         string   `json:"health_path,omitempty"`
}

type Config struct {
	Listen          string    `json:"listen"`
	DefaultProject  string    `json:"default_project"`
	MaxBodyBytes    int64     `json:"max_body_bytes"`
	UpstreamTimeout int       `json:"upstream_timeout_seconds"`
	Projects        []Project `json:"projects"`
}

type ResolvedProject struct {
	Project
	Native          map[Protocol]bool
	BaseURL         *url.URL
	HealthPath      string
	APIKey          string
	AnthropicAPIKey string
}

func Load(path string) (*Config, error) {
	var raw []byte
	var err error
	if path != "" {
		raw, err = os.ReadFile(path)
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	cfg := &Config{}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:7860"
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 64 << 20
	}
	if cfg.UpstreamTimeout <= 0 {
		cfg.UpstreamTimeout = 300
	}
	if len(cfg.Projects) == 0 {
		return nil, errors.New("projects must not be empty")
	}
	seen := map[string]bool{}
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		p.Name = strings.Trim(strings.TrimSpace(p.Name), "/")
		if p.Name == "" || strings.ContainsAny(p.Name, "/?#<>\\") {
			return nil, fmt.Errorf("projects[%d].name must be a single safe path segment", i)
		}
		if p.Name == "v1" || p.Name == "healthz" {
			return nil, fmt.Errorf("project name %q is reserved", p.Name)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("duplicate project %q", p.Name)
		}
		seen[p.Name] = true
		u, err := url.Parse(strings.TrimRight(p.Upstream, "/"))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("project %q has invalid upstream %q", p.Name, p.Upstream)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("project %q upstream scheme must be http or https", p.Name)
		}
		if len(p.Native) == 0 {
			p.Native = []string{string(ProtocolChat)}
		}
		if p.HealthPath == "" {
			p.HealthPath = "/health"
		}
		if !strings.HasPrefix(p.HealthPath, "/") {
			return nil, fmt.Errorf("project %q health_path must start with /", p.Name)
		}
	}
	if cfg.DefaultProject == "" {
		cfg.DefaultProject = cfg.Projects[0].Name
	}
	if !seen[cfg.DefaultProject] {
		return nil, fmt.Errorf("default_project %q is not configured", cfg.DefaultProject)
	}
	return cfg, nil
}

func Resolve(cfg *Config) (map[string]ResolvedProject, error) {
	out := map[string]ResolvedProject{}
	for _, p := range cfg.Projects {
		u, _ := url.Parse(strings.TrimRight(p.Upstream, "/"))
		native := map[Protocol]bool{}
		for _, n := range p.Native {
			proto := Protocol(strings.ToLower(strings.TrimSpace(n)))
			switch proto {
			case ProtocolChat, ProtocolResponses, ProtocolAnthropic:
				native[proto] = true
			default:
				return nil, fmt.Errorf("project %q has unknown protocol %q", p.Name, n)
			}
		}
		if !native[ProtocolChat] {
			return nil, fmt.Errorf("project %q must support chat as a conversion fallback", p.Name)
		}
		rp := ResolvedProject{
			Project:    p,
			Native:     native,
			BaseURL:    u,
			HealthPath: p.HealthPath,
		}
		if p.APIKeyEnv != "" {
			rp.APIKey = os.Getenv(p.APIKeyEnv)
		}
		if p.AnthropicAPIKeyEnv != "" {
			rp.AnthropicAPIKey = os.Getenv(p.AnthropicAPIKeyEnv)
		}
		out[p.Name] = rp
	}
	return out, nil
}
