package config_test

import (
	"os"
	"strings"
	"testing"

	xcfg "github.com/joyboydata/kit/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfig is a minimal config struct used for testing.
type testConfig struct {
	Server   testServerConfig   `mapstructure:"server"`
	Database testDatabaseConfig `mapstructure:"database"`
	Auth     testAuthConfig     `mapstructure:"auth"`
}

type testServerConfig struct {
	Port int    `mapstructure:"port" default:"8080" validate:"gte=1,lte=65535"`
	Host string `mapstructure:"host" default:"localhost"`
}

type testDatabaseConfig struct {
	DSN string `mapstructure:"dsn" default:"postgres://test:test@localhost:5432/test?sslmode=disable" validate:"required"`
}

type testAuthConfig struct {
	ClientID string `mapstructure:"client_id" validate:"required"`
}

// TestLoadValidationError verifies that required fields without defaults cause
// an error.
func TestLoadValidationError(t *testing.T) {
	const envPrefix = "TESTVAL"
	unsetTestEnv(t, envPrefix)
	t.Cleanup(func() { unsetTestEnv(t, envPrefix) })

	cfg := &testConfig{}
	err := xcfg.Load(cfg, envPrefix)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config validation")
}

// TestLoadEnvOverridesDefaults verifies that environment variables override
// default values.
func TestLoadEnvOverridesDefaults(t *testing.T) {
	const envPrefix = "TESTENV"
	unsetTestEnv(t, envPrefix)
	t.Cleanup(func() { unsetTestEnv(t, envPrefix) })

	// Set env vars (the Load function appends _ after prefix automatically)
	t.Setenv(envPrefix+"_SERVER_PORT", "9090")
	t.Setenv(envPrefix+"_SERVER_HOST", "example.com")
	t.Setenv(envPrefix+"_DATABASE_DSN", "postgres://prod:prod@db:5432/prod?sslmode=require")
	t.Setenv(envPrefix+"_AUTH_CLIENT_ID", "my-client-id")

	cfg := &testConfig{}
	err := xcfg.Load(cfg, envPrefix)
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Server.Port)
	assert.Equal(t, "example.com", cfg.Server.Host)
	assert.Equal(t, "postgres://prod:prod@db:5432/prod?sslmode=require", cfg.Database.DSN)
	assert.Equal(t, "my-client-id", cfg.Auth.ClientID)
}

// TestLoadEnvPartialOverride verifies that setting only some env vars leaves
// the rest at their defaults.
func TestLoadEnvPartialOverride(t *testing.T) {
	const envPrefix = "TESTENV"
	unsetTestEnv(t, envPrefix)
	t.Cleanup(func() { unsetTestEnv(t, envPrefix) })

	t.Setenv(envPrefix+"_AUTH_CLIENT_ID", "partial-client-id")
	// No server or database env vars — should use defaults

	cfg := &testConfig{}
	err := xcfg.Load(cfg, envPrefix)
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.Server.Port)                                                                     // default
	assert.Equal(t, "localhost", cfg.Server.Host)                                                               // default
	assert.Equal(t, "postgres://test:test@localhost:5432/test?sslmode=disable", cfg.Database.DSN)               // default
	assert.Equal(t, "partial-client-id", cfg.Auth.ClientID)
}

// TestLoadDifferentPrefix verifies that a different env prefix works correctly.
func TestLoadDifferentPrefix(t *testing.T) {
	const envPrefix = "MYAPP"
	unsetTestEnv(t, envPrefix)
	t.Cleanup(func() { unsetTestEnv(t, envPrefix) })

	t.Setenv(envPrefix+"_AUTH_CLIENT_ID", "prefix-test-client")
	t.Setenv(envPrefix+"_SERVER_PORT", "3000")

	cfg := &testConfig{}
	err := xcfg.Load(cfg, envPrefix)
	require.NoError(t, err)

	assert.Equal(t, 3000, cfg.Server.Port)
	assert.Equal(t, "prefix-test-client", cfg.Auth.ClientID)
}

// unsetTestEnv unsets all environment variables starting with the given prefix
// followed by an underscore.
func unsetTestEnv(t *testing.T, prefix string) {
	fullPrefix := prefix + "_"
	for _, e := range os.Environ() {
		if len(e) >= len(fullPrefix) && e[:len(fullPrefix)] == fullPrefix {
			pair := strings.SplitN(e, "=", 2)
			os.Unsetenv(pair[0])
		}
	}
}