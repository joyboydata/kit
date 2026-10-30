package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	xcfg "github.com/joyboydata/kit/config"
)

func writeConfig(dir, content string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644)
}

func TestConfigSearchDirsUserOverSystem(t *testing.T) {
	user := t.TempDir()
	sys1 := t.TempDir()
	sys2 := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", user)
	t.Setenv("XDG_CONFIG_DIRS", sys1+":"+sys2)
	xdg.Reload()

	dirs := xcfg.ConfigSearchDirs("myapp")

	require.Equal(t, []string{
		filepath.Join(user, "myapp"),
		filepath.Join(sys1, "myapp"),
		filepath.Join(sys2, "myapp"),
		filepath.Join("/etc", "myapp"),
	}, dirs)
}

func TestAddConfigPathsOrder(t *testing.T) {
	user := t.TempDir()
	sys := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", user)
	t.Setenv("XDG_CONFIG_DIRS", sys)
	xdg.Reload()

	v := viper.New()
	xcfg.AddConfigPaths(v, "myapp")

	// First registered = highest viper priority: user dir must come first,
	// legacy /etc/<app> last. Viper exposes paths via ConfigFileUsed only
	// after a read, so verify indirectly by re-reading search order through
	// a fresh viper that finds a user-level config first.
	require.NotPanics(t, func() {
		v.SetConfigName("config")
	})

	// Direct order check via ConfigSearchDirs (same order AddConfigPaths uses).
	want := xcfg.ConfigSearchDirs("myapp")
	require.Equal(t, filepath.Join(user, "myapp"), want[0])
	require.Equal(t, filepath.Join("/etc", "myapp"), want[len(want)-1])

	// Functional check: user config shadows system config.
	require.NoError(t, writeConfig(filepath.Join(user, "myapp"), "value: from-user\n"))
	require.NoError(t, writeConfig(filepath.Join(sys, "myapp"), "value: from-system\n"))

	v2 := viper.New()
	v2.SetConfigName("config")
	v2.SetConfigType("yaml")
	xcfg.AddConfigPaths(v2, "myapp")
	require.NoError(t, v2.ReadInConfig())
	require.Equal(t, "from-user", v2.GetString("value"))
}
