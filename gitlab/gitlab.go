package gitlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// Service represents a service configuration
type Service struct {
	Name          string `yaml:"name"`
	Directory     string `yaml:"directory"`
	GitlabProject string `yaml:"gitlab_project"`
	Group         string `yaml:"group"`
	Sequential    bool   `yaml:"sequential"`
}

// PipelineResponse represents GitLab pipeline creation response
type PipelineResponse struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

// ProjectVariable represents a GitLab project variable
type ProjectVariable struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	VariableType     string `json:"variable_type"`
	Protected        bool   `json:"protected"`
	Masked           bool   `json:"masked"`
	EnvironmentScope string `json:"environment_scope"`
}

const (
	colorBlue  = "\033[34m"
	colorGreen = "\033[32m"
	colorReset = "\033[0m"
)

// CreatePipelines creates GitLab pipelines according to service configuration
func CreatePipelines(services []Service, ref string, helmNamespace string) error {
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	if gitlabToken == "" {
		return fmt.Errorf("GITLAB_TOKEN environment variable is not set")
	}

	gitlabURI := os.Getenv("GITLAB_URI")
	if gitlabURI == "" {
		return fmt.Errorf("GITLAB_URI environment variable is not set")
	}

	// Group services by their group attribute
	groups := make(map[string][]Service)
	var sequentialServices []Service

	for _, service := range services {
		if service.Sequential {
			sequentialServices = append(sequentialServices, service)
		} else if service.Group != "" {
			groups[service.Group] = append(groups[service.Group], service)
		} else {
			// Treat ungrouped non-sequential services as individual sequential services
			sequentialServices = append(sequentialServices, service)
		}
	}

	// Process sequential services first
	for _, service := range sequentialServices {
		fmt.Printf("\n%sStarting pipeline for sequential service: %s on tag: %s%s\n", colorBlue, service.Name, ref, colorReset)

		pipelineID, err := createPipeline(service, gitlabURI, gitlabToken, ref, helmNamespace)
		if err != nil {
			return fmt.Errorf("failed to create pipeline for %s: %v", service.Name, err)
		}

		// Wait for pipeline to complete
		if err := waitForPipeline(service, gitlabURI, gitlabToken, pipelineID); err != nil {
			return fmt.Errorf("pipeline failed for %s: %v", service.Name, err)
		}
	}

	// Process grouped services in parallel
	for groupName, groupServices := range groups {
		fmt.Printf("\n%sStarting pipelines for group: %s on tag: %s%s\n", colorBlue, groupName, ref, colorReset)

		var wg sync.WaitGroup
		errors := make(chan error, len(groupServices))

		for _, service := range groupServices {
			wg.Add(1)
			go func(svc Service) {
				defer wg.Done()

				pipelineID, err := createPipeline(svc, gitlabURI, gitlabToken, ref, helmNamespace)
				if err != nil {
					errors <- fmt.Errorf("failed to create pipeline for %s: %v", svc.Name, err)
					return
				}

				// Wait for pipeline to complete
				if err := waitForPipeline(svc, gitlabURI, gitlabToken, pipelineID); err != nil {
					errors <- fmt.Errorf("pipeline failed for %s: %v", svc.Name, err)
					return
				}
			}(service)
		}

		wg.Wait()
		close(errors)

		// Check for errors
		for err := range errors {
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// createPipeline creates a single pipeline
func createPipeline(service Service, gitlabURI, gitlabToken, ref, helmNamespace string) (int, error) {
	// URL encode the project path
	projectPath := url.QueryEscape(service.GitlabProject)

	// Prepare the request
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/pipeline", gitlabURI, projectPath)

	// Build request body
	requestBody := map[string]interface{}{
		"ref": ref,
		"variables": []map[string]string{
			{"key": "CI_PIPELINE_SOURCE", "value": "web"},
			{"key": "HELM_NAMESPACE", "value": helmNamespace},
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return 0, err
	}

	req.Header.Set("PRIVATE-TOKEN", gitlabToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	if resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("failed to create pipeline: %s", string(body))
	}

	var pipelineResp PipelineResponse
	if err := json.Unmarshal(body, &pipelineResp); err != nil {
		return 0, err
	}

	fmt.Printf("  Created pipeline for %s: %s\n", service.Name, pipelineResp.WebURL)
	return pipelineResp.ID, nil
}

// waitForPipeline waits for a pipeline to complete
func waitForPipeline(service Service, gitlabURI, gitlabToken string, pipelineID int) error {
	projectPath := url.QueryEscape(service.GitlabProject)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d", gitlabURI, projectPath, pipelineID)

	client := &http.Client{Timeout: 30 * time.Second}

	// Poll every 30 seconds
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	maxDuration := 60 * time.Minute

	for {
		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			return err
		}

		req.Header.Set("PRIVATE-TOKEN", gitlabToken)

		resp, err := client.Do(req)
		if err != nil {
			return err
		}

		body, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		var pipelineResp PipelineResponse
		if err := json.Unmarshal(body, &pipelineResp); err != nil {
			return err
		}

		switch pipelineResp.Status {
		case "success":
			fmt.Printf("  %s✓ Pipeline completed successfully for %s%s\n", colorGreen, service.Name, colorReset)
			return nil
		case "failed", "canceled", "skipped":
			return fmt.Errorf("pipeline %s for %s", pipelineResp.Status, service.Name)
		case "running", "pending", "created":
			fmt.Printf("  Pipeline for %s is %s...\n", service.Name, pipelineResp.Status)
		}

		if time.Since(startTime) > maxDuration {
			return fmt.Errorf("pipeline timeout for %s", service.Name)
		}

		<-ticker.C
	}
}
