package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

// EnvLookup is a function that looks up an environment variable by key.
// It returns the value and whether the variable was found,
// matching the signature of os.LookupEnv.
type EnvLookup func(key string) (string, bool)

// SecretResolveFunc resolves a vault reference (e.g. "1pw://vault/item/field")
// to its plaintext value. Non-references are returned unchanged.
type SecretResolveFunc func(ref string) (string, error)

// EnvField maps a config struct field to its environment variable suffix.
// For example, {Suffix: "TOKEN", Set: func(c *MyConfig, v string) { c.Token = v }}
// will check for PROVIDER_TOKEN (global) and PROVIDER_NAME_TOKEN (per-instance).
type EnvField[C any] struct {
	Suffix string
	Set    func(c *C, v string)
	Get    func(c C) string // optional: read the current value (needed for vault resolution)
}

// ApplyEnvOverrides applies environment variable overrides to a config struct.
// It checks global variables first (PREFIX_SUFFIX), then per-instance variables
// (PREFIX_NAME_SUFFIX). Per-instance overrides take precedence.
//
// The lookup parameter controls how environment variables are resolved.
// When nil, os.LookupEnv is used. Pass a custom function to implement
// per-project scoping or other lookup strategies.
func ApplyEnvOverrides[C any](cfg *C, name, envPrefix string, fields []EnvField[C], lookup EnvLookup) {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	// Global overrides: PREFIX_SUFFIX
	for _, f := range fields {
		if v, ok := lookup(envPrefix + f.Suffix); ok {
			f.Set(cfg, v)
		}
	}

	// Per-instance overrides: PREFIX_NAME_SUFFIX (takes precedence)
	if name != "" {
		instancePrefix := envPrefix + strings.ToUpper(name) + "_"
		for _, f := range fields {
			if v, ok := lookup(instancePrefix + f.Suffix); ok {
				f.Set(cfg, v)
			}
		}
	}
}

// InstanceSpec defines how to load and build instances from config entries.
type InstanceSpec[C any, I any] struct {
	// Section is the YAML key in .humanconfig (e.g. "githubs", "jiras").
	Section string

	// EnvPrefix is the prefix for environment variables (e.g. "GITHUB_", "JIRA_").
	EnvPrefix string

	// EnvFields maps config struct fields to env var suffixes.
	EnvFields []EnvField[C]

	// DefaultURL is set on configs with an empty URL before env overrides.
	// Leave empty if no default (e.g. Jira requires explicit URL).
	DefaultURL string

	// Lookup overrides how environment variables are resolved.
	// When nil, os.LookupEnv is used. Set this to a per-project scoped
	// lookup function to support multi-project token isolation.
	Lookup EnvLookup

	// GetName returns the instance name from a config entry.
	GetName func(C) string

	// SetURL sets the URL on the config. Nil if the config has no URL field.
	SetURL func(*C, string)

	// GetURL returns the URL from a config entry. Nil if the config has no URL field.
	GetURL func(C) string

	// Build creates an instance from a config entry. Return (zero, false) to skip
	// the entry (e.g. missing required credentials).
	Build func(C) (I, bool)

	// SecretResolver resolves vault references in config field values.
	// When nil, no vault resolution is performed and values are used as-is.
	// Set this to vault.Resolver.Resolve to enable 1pw:// references.
	SecretResolver SecretResolveFunc
}

// LoadInstances reads configs from a .humanconfig file, applies env overrides,
// and builds instances using the provided spec.
func LoadInstances[C any, I any](dir string, spec InstanceSpec[C, I]) ([]I, error) {
	var configs []C
	if err := UnmarshalSection(dir, spec.Section, &configs); err != nil {
		return nil, err
	}

	instances := make([]I, 0, len(configs))
	for _, cfg := range configs {
		// Apply default URL if configured.
		if spec.DefaultURL != "" && spec.SetURL != nil && spec.GetURL != nil {
			if spec.GetURL(cfg) == "" {
				spec.SetURL(&cfg, spec.DefaultURL)
			}
		}

		ApplyEnvOverrides(&cfg, spec.GetName(cfg), spec.EnvPrefix, spec.EnvFields, spec.Lookup)

		// Resolve vault references in config fields (e.g. 1pw://...).
		if spec.SecretResolver != nil {
			if err := resolveSecrets(&cfg, spec.EnvFields, spec.SecretResolver); err != nil {
				return nil, err
			}
		}

		inst, ok := spec.Build(cfg)
		if !ok {
			// A configured entry that fails to build is almost always an
			// instance-name/env-var mismatch. Surface it so the instance does
			// not vanish silently from `human tracker list`.
			warnSkippedInstance(spec, cfg)
			continue
		}
		instances = append(instances, inst)
	}
	return instances, nil
}

// warnSkippedInstance emits a diagnostic when a configured entry was dropped by
// Build. Every entry in configs came straight from YAML, so a rejected build
// means "configured but incomplete" and warrants naming the culprit and the
// exact env vars that would satisfy it.
func warnSkippedInstance[C any, I any](spec InstanceSpec[C, I], cfg C) {
	name := ""
	if spec.GetName != nil {
		name = spec.GetName(cfg)
	}

	// Collect every credential field that resolves empty — these are the
	// reasons Build rejected the entry.
	var missing []string
	var hints []string
	for _, f := range spec.EnvFields {
		if f.Get == nil || f.Get(cfg) != "" {
			continue
		}
		missing = append(missing, f.Suffix)

		globalVar := spec.EnvPrefix + f.Suffix
		instanceVar := globalVar
		if name != "" {
			instanceVar = spec.EnvPrefix + strings.ToUpper(name) + "_" + f.Suffix
		}
		hints = append(hints, fmt.Sprintf(
			"set %s (or %s) or add %s: to .humanconfig",
			instanceVar, globalVar, strings.ToLower(f.Suffix),
		))
	}

	if len(missing) == 0 {
		// Build rejected for a reason not tied to an empty EnvField (future
		// provider): still surface that the entry was dropped.
		log.Warn().
			Str("section", spec.Section).
			Str("instance", name).
			Msg("skipped configured instance: required configuration is incomplete")
		return
	}

	log.Warn().
		Str("section", spec.Section).
		Str("instance", name).
		Strs("missing", missing).
		Strs("hints", hints).
		Msg("skipped configured instance: unresolved credentials")
}

// resolveSecrets iterates over EnvFields that have a Get function and resolves
// vault references through the provided resolver. Fields without Get are skipped.
func resolveSecrets[C any](cfg *C, fields []EnvField[C], resolve SecretResolveFunc) error {
	for _, f := range fields {
		if f.Get == nil {
			continue
		}
		val := f.Get(*cfg)
		if val == "" {
			continue
		}
		resolved, err := resolve(val)
		if err != nil {
			return err
		}
		if resolved != val {
			f.Set(cfg, resolved)
		}
	}
	return nil
}
