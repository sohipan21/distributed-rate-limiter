// Package config loads policies and api keys from a yaml file, so limits
// change with an edit and a reload instead of a redeploy
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
)

type Config struct {
	Policies *policy.Policies
	APIKeys  map[string]APIKey
}

// what an api key resolves to; tier is looked up here, never trusted from
// the client
type APIKey struct {
	Account string `yaml:"account"`
	Tier    string `yaml:"tier"`
}

// yaml.v3 has no native duration support
type duration time.Duration

func (d *duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("bad duration %q: %w", s, err)
	}
	*d = duration(parsed)
	return nil
}

type limitSpec struct {
	Limit     int      `yaml:"limit"`
	Window    duration `yaml:"window"`
	Burst     int      `yaml:"burst"`
	Algorithm string   `yaml:"algorithm"`
}

type fileSchema struct {
	Policies struct {
		Default   *limitSpec           `yaml:"default"`
		Tiers     map[string]limitSpec `yaml:"tiers"`
		Endpoints map[string]limitSpec `yaml:"endpoints"`
	} `yaml:"policies"`
	APIKeys map[string]APIKey `yaml:"api_keys"`
}

// Load reads path and builds validated policies; bad config fails here,
// loudly, not at request time
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f fileSchema
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if f.Policies.Default == nil {
		return nil, fmt.Errorf("config %s: policies.default is required", path)
	}

	var rules []policy.Rule
	for tier, spec := range f.Policies.Tiers {
		rules = append(rules, policy.Rule{Tier: tier, Limit: toLimit(spec)})
	}
	for endpoint, spec := range f.Policies.Endpoints {
		rules = append(rules, policy.Rule{Endpoint: endpoint, Limit: toLimit(spec)})
	}

	// NewPolicies validates limits, windows, and algorithm names
	p, err := policy.NewPolicies(toLimit(*f.Policies.Default), rules...)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &Config{Policies: p, APIKeys: f.APIKeys}, nil
}

func toLimit(s limitSpec) policy.Limit {
	algo := limiter.TokenBucketAlgorithm
	if s.Algorithm != "" {
		algo = limiter.Algorithm(s.Algorithm)
	}
	return policy.Limit{
		Algorithm: algo,
		Config:    limiter.Config{Limit: s.Limit, Window: time.Duration(s.Window), Burst: s.Burst},
	}
}
