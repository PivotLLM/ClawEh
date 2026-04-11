package configstore

import (
	"errors"
	"os"
	"path/filepath"

	clawconfig "github.com/PivotLLM/ClawEh/pkg/config"
)

const (
	configDirName  = ".claw"
	configFileName = "config.json"
)

func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDirName), nil
}

func Load() (*clawconfig.Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return clawconfig.LoadConfig(path)
}

func Save(cfg *clawconfig.Config) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	return clawconfig.SaveConfig(path, cfg)
}
