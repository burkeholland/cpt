package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type config struct {
	LastModel string `json:"last_model"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cpt", "config.json")
}

func loadConfig() config {
	var cfg config
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	json.Unmarshal(data, &cfg)
	return cfg
}

func saveConfig(cfg config) {
	p := configPath()
	os.MkdirAll(filepath.Dir(p), 0755)
	data, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	os.WriteFile(p, data, 0644)
}
