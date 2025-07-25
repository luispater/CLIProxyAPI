package config

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
)

// Config represents the application's configuration, loaded from a YAML file.
type Config struct {
	// Port is the network port on which the API server will listen.
	Port int `yaml:"port"`
	// AuthDir is the directory where authentication token files are stored.
	AuthDir string `yaml:"auth-dir"`
	// Debug enables or disables debug-level logging and other debug features.
	Debug bool `yaml:"debug"`
	// ProxyUrl is the URL of an optional proxy server to use for outbound requests.
	ProxyUrl string `yaml:"proxy-url"`
	// ApiKeys is a list of keys for authenticating clients to this proxy server.
	ApiKeys []string `yaml:"api-keys"`
	// QuotaExceeded defines the behavior when a quota is exceeded.
	QuotaExceeded ConfigQuotaExceeded `yaml:"quota-exceeded"`
	// GlAPIKey is the API key for the generative language API.
	GlAPIKey []string `yaml:"generative-language-api-key"`
}

type ConfigQuotaExceeded struct {
	// SwitchProject indicates whether to automatically switch to another project when a quota is exceeded.
	SwitchProject bool `yaml:"switch-project"`
	// SwitchPreviewModel indicates whether to automatically switch to a preview model when a quota is exceeded.
	SwitchPreviewModel bool `yaml:"switch-preview-model"`
}

// LoadConfig reads a YAML configuration file from the given path,
// unmarshals it into a Config struct, and returns it.
func LoadConfig(configFile string) (*Config, error) {
	// Read the entire configuration file into memory.
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Unmarshal the YAML data into the Config struct.
	var config Config
	if err = yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Return the populated configuration struct.
	return &config, nil
}
