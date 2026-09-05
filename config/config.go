// Package config provides an opinionated, reusable configuration loader.
//
// Usage:
//
//	type MyConfig struct {
//		Server ServerConfig `mapstructure:"server"`
//	}
//
//	type ServerConfig struct {
//		Port int `mapstructure:"port" default:"8080" validate:"gte=1,lte=65535"`
//	}
//
//	cfg := &MyConfig{}
//	if err := config.Load(cfg, "APLIQ"); err != nil {
//	    log.Fatal(err)
//	}
//
// Load reads configuration in the following priority order:
//
//	1. Default values from `default:"..."` struct tags (lowest)
//	2. config.yaml (from . or ./config/, non-fatal if absent)
//	3. Environment variables prefixed with the given prefix (highest)
//
// After loading, it validates the config using `validate:"..."` struct tags.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
)

// Load reads configuration into cfg using three-tier merge.
//
// Priority (lowest to highest):
//  1. Struct tag defaults (`default:"..."`)
//  2. config.yaml (from . or ./config/)
//  3. Environment variables prefixed with envPrefix
//
// After loading, the struct is validated via `validate:"..."` struct tags.
// cfg must be a non-nil pointer to a struct.
func Load(cfg any, envPrefix string) error {
	// Step 1: Apply defaults from struct tags
	if err := defaults.Set(cfg); err != nil {
		return fmt.Errorf("set defaults: %w", err)
	}

	// Step 2: Read config.yaml (if it exists)
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("read config file: %w", err)
		}
		// config file not found is okay — fall through to env
	}

	// Step 3: Inject environment variables into Viper's store.
	//
	// Viper's AutomaticEnv only resolves env vars during per-key Get() calls,
	// not during Unmarshal (which uses AllSettings() internally).
	// To ensure nested env vars (e.g., APLIQ_AUTH_CLIENT_ID) are picked up
	// during Unmarshal, we explicitly inject them.
	const delimiter = "_"
	prefix := envPrefix + delimiter
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, prefix) {
			continue
		}
		pair := strings.SplitN(e, "=", 2)
		if len(pair) != 2 {
			continue
		}
		envKey := strings.TrimPrefix(pair[0], prefix)
		// APLIQ_AUTH_CLIENT_ID → auth.client_id
		// Replace only the first delimiter to separate section from field.
		parts := strings.SplitN(envKey, delimiter, 2)
		viperKey := strings.ToLower(parts[0]) + "." + parts[1]
		v.Set(viperKey, pair[1])
	}

	// Step 4: Unmarshal viper values into config (overrides defaults)
	if err := v.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}

	// Step 5: Validate all required fields
	validate := validator.New()
	if err := validate.Struct(cfg); err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	return nil
}