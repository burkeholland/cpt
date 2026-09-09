package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type config struct {
	LastModel       string   `json:"last_model"`
	Models          []string `json:"models,omitempty"`
	ModelsUpdatedAt int64    `json:"models_updated_at,omitempty"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cpt", "config.json")
}

func loadConfig() (config, error) {
	var cfg config
	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func saveConfig(cfg config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := atomicWriteFile(p, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func updateConfig(update func(*config)) error {
	cfg, err := loadConfig()
	if err != nil {
		cfg = config{}
	}
	update(&cfg)
	return saveConfig(cfg)
}
