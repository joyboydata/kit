package config

import (
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/spf13/viper"
)

// ConfigSearchDirs returns the ordered directories to search for an app's
// config files: the user dir (xdg.ConfigHome/app) first, then each system
// dir (xdg.ConfigDirs/app) in order, then the legacy /etc/<app> last.
func ConfigSearchDirs(app string) []string {
	dirs := make([]string, 0, len(xdg.ConfigDirs)+2)
	dirs = append(dirs, filepath.Join(xdg.ConfigHome, app))
	for _, d := range xdg.ConfigDirs {
		dirs = append(dirs, filepath.Join(d, app))
	}
	legacy := filepath.Join("/etc", app)
	if len(dirs) == 0 || dirs[len(dirs)-1] != legacy {
		dirs = append(dirs, legacy)
	}
	return dirs
}

// AddConfigPaths registers each ConfigSearchDirs(app) dir on v in order.
// Viper gives higher priority to earlier-added paths, so the user dir wins
// over system dirs and /etc/<app>.
func AddConfigPaths(v *viper.Viper, app string) {
	for _, d := range ConfigSearchDirs(app) {
		v.AddConfigPath(d)
	}
}
