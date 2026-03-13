package config

import (
	"gopkg.in/yaml.v2"
	"io/ioutil"
)

// Service represents a service configuration
type Service struct {
	Name          string `yaml:"name"`
	Directory     string `yaml:"directory"`
	GitlabProject string `yaml:"gitlab_project"`
	IsMesh        bool   `yaml:"is_mesh"`
	IsLibrary     bool   `yaml:"is_library"`
}

// ArtifactExclusion defines an artifact whose version should not be updated anywhere
type ArtifactExclusion struct {
	GroupID    string `yaml:"groupId"`
	ArtifactID string `yaml:"artifactId"`
}

// Config represents the deploy configuration with new structure
type Config struct {
	TaskURLPrefix      string               `yaml:"task_url_prefix"`
	SkipVersionUpdate  []ArtifactExclusion  `yaml:"skip_version_update"`
	SkipProperties     []string             `yaml:"skip_properties"`
	Sequential         []Service            `yaml:"sequential"`
	Groups             map[string][]Service `yaml:"groups"`
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

// GetAllServices returns all services as a flat list with metadata
func (c *Config) GetAllServices() []ServiceWithMeta {
	var services []ServiceWithMeta

	// Add sequential services
	for _, svc := range c.Sequential {
		services = append(services, ServiceWithMeta{
			Service:    svc,
			Sequential: true,
			Group:      "",
		})
	}

	// Add grouped services
	for groupName, groupServices := range c.Groups {
		for _, svc := range groupServices {
			services = append(services, ServiceWithMeta{
				Service:    svc,
				Sequential: false,
				Group:      groupName,
			})
		}
	}

	return services
}

// ServiceWithMeta includes service with its execution metadata
type ServiceWithMeta struct {
	Service
	Sequential bool
	Group      string
}
