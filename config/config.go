package config

import (
	"io/ioutil"
	"gopkg.in/yaml.v2"
)

// Service represents a service configuration
type Service struct {
	Name          string `yaml:"name"`
	Directory     string `yaml:"directory"`
	GitlabProject string `yaml:"gitlab_project"`
	Group         string `yaml:"group"`
	Sequential    bool   `yaml:"sequential"`
}

// Config represents the deploy configuration
type Config struct {
	Services []Service `yaml:"services"`
}

// ReadYAMLConfig reads and parses the YAML configuration file
func ReadYAMLConfig(filename string) (*Config, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}