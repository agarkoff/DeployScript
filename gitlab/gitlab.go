package gitlab

import (
	"bytes"
	"deploy/config"
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

// BridgeResponse represents a GitLab bridge job (trigger for downstream pipeline)
type BridgeResponse struct {
	ID                 int               `json:"id"`
	Name               string            `json:"name"`
	Status             string            `json:"status"`
	DownstreamPipeline *PipelineResponse `json:"downstream_pipeline"`
}

// JobResponse represents a GitLab pipeline job
type JobResponse struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
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

// CreatePipelinesFromConfig creates GitLab pipelines using the new config structure
func CreatePipelinesFromConfig(cfg *config.Config, ref string, helmNamespace string, runJob string) error {
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	if gitlabToken == "" {
		return fmt.Errorf("GITLAB_TOKEN environment variable is not set")
	}

	gitlabURI := os.Getenv("GITLAB_URI")
	if gitlabURI == "" {
		return fmt.Errorf("GITLAB_URI environment variable is not set")
	}

	// Process sequential services first
	for _, service := range cfg.Sequential {
		fmt.Printf("\n%sStarting pipeline for sequential service: %s on tag: %s%s\n", colorBlue, service.Name, ref, colorReset)

		pipelineID, err := createPipelineForService(service, gitlabURI, gitlabToken, ref, helmNamespace)
		if err != nil {
			return fmt.Errorf("failed to create pipeline for %s: %v", service.Name, err)
		}

		// Wait for pipeline to complete
		if err := waitForPipelineForService(service, gitlabURI, gitlabToken, pipelineID, runJob); err != nil {
			return fmt.Errorf("pipeline failed for %s: %v", service.Name, err)
		}
	}

	// Process each group in sequence, but services within a group in parallel
	for groupName, groupServices := range cfg.Groups {
		fmt.Printf("\n%sStarting pipelines for group: %s on tag: %s%s\n", colorBlue, groupName, ref, colorReset)

		var wg sync.WaitGroup
		errors := make(chan error, len(groupServices))

		for _, service := range groupServices {
			wg.Add(1)
			go func(svc config.Service) {
				defer wg.Done()

				pipelineID, err := createPipelineForService(svc, gitlabURI, gitlabToken, ref, helmNamespace)
				if err != nil {
					errors <- fmt.Errorf("failed to create pipeline for %s: %v", svc.Name, err)
					return
				}

				// Wait for pipeline to complete
				if err := waitForPipelineForService(svc, gitlabURI, gitlabToken, pipelineID, runJob); err != nil {
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

// ContinuePipelinesFromConfig checks pipeline statuses and re-runs failed/missing ones.
// For each service it checks pipelines from the last 24 hours on the given ref.
// If a pipeline succeeded (including downstream "deploy helm" where applicable), it's skipped.
// Otherwise, a new pipeline is created and monitored.
func ContinuePipelinesFromConfig(cfg *config.Config, ref string, helmNamespace string, runJob string) error {
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	if gitlabToken == "" {
		return fmt.Errorf("GITLAB_TOKEN environment variable is not set")
	}

	gitlabURI := os.Getenv("GITLAB_URI")
	if gitlabURI == "" {
		return fmt.Errorf("GITLAB_URI environment variable is not set")
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// Process sequential services first
	for _, service := range cfg.Sequential {
		needsRun, err := checkServiceNeedsRerun(client, gitlabURI, gitlabToken, service.GitlabProject, ref, service.Name)
		if err != nil {
			return fmt.Errorf("failed to check pipeline status for %s: %v", service.Name, err)
		}

		if !needsRun {
			fmt.Printf("  %s✓ %s already deployed successfully, skipping%s\n", colorGreen, service.Name, colorReset)
			continue
		}

		fmt.Printf("\n%sRe-running pipeline for sequential service: %s on tag: %s%s\n", colorBlue, service.Name, ref, colorReset)

		pipelineID, err := createPipelineForService(service, gitlabURI, gitlabToken, ref, helmNamespace)
		if err != nil {
			return fmt.Errorf("failed to create pipeline for %s: %v", service.Name, err)
		}

		if err := waitForPipelineForService(service, gitlabURI, gitlabToken, pipelineID, runJob); err != nil {
			return fmt.Errorf("pipeline failed for %s: %v", service.Name, err)
		}
	}

	// Process each group in sequence, but services within a group in parallel
	for groupName, groupServices := range cfg.Groups {
		// Check which services in this group need re-running
		var servicesToRun []config.Service
		for _, service := range groupServices {
			needsRun, err := checkServiceNeedsRerun(client, gitlabURI, gitlabToken, service.GitlabProject, ref, service.Name)
			if err != nil {
				return fmt.Errorf("failed to check pipeline status for %s: %v", service.Name, err)
			}
			if !needsRun {
				fmt.Printf("  %s✓ %s already deployed successfully, skipping%s\n", colorGreen, service.Name, colorReset)
			} else {
				servicesToRun = append(servicesToRun, service)
			}
		}

		if len(servicesToRun) == 0 {
			continue
		}

		fmt.Printf("\n%sRe-running pipelines for group: %s on tag: %s%s\n", colorBlue, groupName, ref, colorReset)

		var wg sync.WaitGroup
		errors := make(chan error, len(servicesToRun))

		for _, service := range servicesToRun {
			wg.Add(1)
			go func(svc config.Service) {
				defer wg.Done()

				pipelineID, err := createPipelineForService(svc, gitlabURI, gitlabToken, ref, helmNamespace)
				if err != nil {
					errors <- fmt.Errorf("failed to create pipeline for %s: %v", svc.Name, err)
					return
				}

				if err := waitForPipelineForService(svc, gitlabURI, gitlabToken, pipelineID, runJob); err != nil {
					errors <- fmt.Errorf("pipeline failed for %s: %v", svc.Name, err)
					return
				}
			}(service)
		}

		wg.Wait()
		close(errors)

		for err := range errors {
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// checkServiceNeedsRerun checks if a service needs a pipeline re-run.
// Returns true if no successful pipeline exists in the last 24 hours for the given ref,
// or if the downstream "deploy helm" job failed.
func checkServiceNeedsRerun(client *http.Client, gitlabURI, gitlabToken, gitlabProject, ref, serviceName string) (bool, error) {
	projectPath := url.QueryEscape(gitlabProject)
	updatedAfter := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)

	// Get recent pipelines for this ref
	pipelinesURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines?ref=%s&updated_after=%s&order_by=id&sort=desc",
		gitlabURI, projectPath, url.QueryEscape(ref), url.QueryEscape(updatedAfter))

	body, err := gitlabGet(client, pipelinesURL, gitlabToken)
	if err != nil {
		return true, fmt.Errorf("failed to list pipelines: %v", err)
	}

	var pipelines []PipelineResponse
	if err := json.Unmarshal(body, &pipelines); err != nil {
		return true, fmt.Errorf("failed to parse pipelines: %v", err)
	}

	if len(pipelines) == 0 {
		fmt.Printf("  No pipelines found for %s on %s in last 24h\n", serviceName, ref)
		return true, nil
	}

	// Check the most recent pipeline
	latestPipeline := pipelines[0]

	// Check for downstream pipeline via bridges
	bridgesURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/bridges",
		gitlabURI, projectPath, latestPipeline.ID)

	bridgesBody, err := gitlabGet(client, bridgesURL, gitlabToken)
	if err != nil {
		return true, fmt.Errorf("failed to check bridges: %v", err)
	}

	var bridges []BridgeResponse
	if err := json.Unmarshal(bridgesBody, &bridges); err != nil {
		return true, fmt.Errorf("failed to parse bridges: %v", err)
	}

	// Look for "deploy service" bridge with downstream pipeline
	for _, bridge := range bridges {
		if bridge.Name == "deploy service" && bridge.DownstreamPipeline != nil {
			// Has downstream — check "deploy helm" job
			jobsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs",
				gitlabURI, projectPath, bridge.DownstreamPipeline.ID)

			jobsBody, err := gitlabGet(client, jobsURL, gitlabToken)
			if err != nil {
				return true, fmt.Errorf("failed to check downstream jobs: %v", err)
			}

			var jobs []JobResponse
			if err := json.Unmarshal(jobsBody, &jobs); err != nil {
				return true, fmt.Errorf("failed to parse downstream jobs: %v", err)
			}

			for _, job := range jobs {
				if job.Name == "deploy helm" {
					if job.Status == "success" {
						return false, nil
					}
					fmt.Printf("  Pipeline %d for %s: deploy helm is %s\n", latestPipeline.ID, serviceName, job.Status)
					return true, nil
				}
			}

			fmt.Printf("  Pipeline %d for %s: deploy helm job not found in downstream\n", latestPipeline.ID, serviceName)
			return true, nil
		}
	}

	// No downstream pipeline — check main pipeline status
	if latestPipeline.Status == "success" {
		return false, nil
	}

	fmt.Printf("  Pipeline %d for %s is %s\n", latestPipeline.ID, serviceName, latestPipeline.Status)
	return true, nil
}

// CreatePipelines creates GitLab pipelines according to service configuration (legacy function)
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
		if err := waitForPipeline(service, gitlabURI, gitlabToken, pipelineID, ""); err != nil {
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
				if err := waitForPipeline(svc, gitlabURI, gitlabToken, pipelineID, ""); err != nil {
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

// createPipelineForService creates a pipeline for config.Service
func createPipelineForService(service config.Service, gitlabURI, gitlabToken, ref, helmNamespace string) (int, error) {
	// Convert to gitlab.Service for compatibility
	gitlabService := Service{
		Name:          service.Name,
		Directory:     service.Directory,
		GitlabProject: service.GitlabProject,
	}
	return createPipeline(gitlabService, gitlabURI, gitlabToken, ref, helmNamespace)
}

// waitForPipelineForService waits for a pipeline for config.Service
func waitForPipelineForService(service config.Service, gitlabURI, gitlabToken string, pipelineID int, runJob string) error {
	// Convert to gitlab.Service for compatibility
	gitlabService := Service{
		Name:          service.Name,
		Directory:     service.Directory,
		GitlabProject: service.GitlabProject,
	}
	return waitForPipeline(gitlabService, gitlabURI, gitlabToken, pipelineID, runJob)
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

// gitlabGet performs a GET request to GitLab API with retry logic
func gitlabGet(client *http.Client, url, token string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}

// pollResult represents the outcome of a single polling iteration
type pollResult int

const (
	pollContinue pollResult = iota // keep polling
	pollSuccess                    // done successfully
	pollFailed                     // terminal failure, no retry
)

// terminalError represents a non-retryable error (pipeline/job failed or canceled)
type terminalError struct {
	message string
}

func (e *terminalError) Error() string {
	return e.message
}

// waitForPipeline waits for a pipeline to complete.
// If the main pipeline has a "deploy service" bridge job, it waits for the
// "deploy helm" job in the downstream pipeline to succeed.
// Otherwise, it waits for the main pipeline to succeed.
func waitForPipeline(service Service, gitlabURI, gitlabToken string, pipelineID int, runJob string) error {
	projectPath := url.QueryEscape(service.GitlabProject)
	client := &http.Client{Timeout: 30 * time.Second}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	maxDuration := 60 * time.Minute
	maxRetryDuration := 60 * time.Minute
	var firstErrorTime time.Time

	downstreamPipelineID := 0
	runJobTriggered := false

	for {
		var result pollResult
		var err error

		if downstreamPipelineID > 0 {
			// After finding downstream pipeline, trigger the manual job if requested
			if runJob != "" && !runJobTriggered {
				if triggerErr := triggerManualJob(client, gitlabURI, gitlabToken, projectPath, pipelineID, runJob, service.Name); triggerErr != nil {
					fmt.Printf("  Warning: failed to trigger job \"%s\" for %s: %v\n", runJob, service.Name, triggerErr)
				}
				runJobTriggered = true
			}
			result, err = pollDeployHelmJob(client, gitlabURI, gitlabToken, projectPath, downstreamPipelineID, service.Name)
		} else {
			result, downstreamPipelineID, err = pollMainPipeline(client, gitlabURI, gitlabToken, projectPath, pipelineID, service.Name)
		}

		if result == pollSuccess {
			return nil
		}

		if err != nil {
			// Terminal errors (failed/canceled) — return immediately
			if _, ok := err.(*terminalError); ok {
				return err
			}
			// Transient errors — retry with timeout
			if firstErrorTime.IsZero() {
				firstErrorTime = time.Now()
			}
			if time.Since(firstErrorTime) > maxRetryDuration {
				return fmt.Errorf("pipeline monitoring failed for %s, errors for over %v: %v", service.Name, maxRetryDuration, err)
			}
			fmt.Printf("  Warning: %v\n", err)
		} else {
			firstErrorTime = time.Time{}
		}

		if time.Since(startTime) > maxDuration {
			return fmt.Errorf("pipeline timeout for %s", service.Name)
		}

		<-ticker.C
	}
}

// triggerManualJob finds and triggers a manual or delayed job in the pipeline
func triggerManualJob(client *http.Client, gitlabURI, gitlabToken, projectPath string, pipelineID int, jobName string, serviceName string) error {
	jobsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs", gitlabURI, projectPath, pipelineID)
	body, err := gitlabGet(client, jobsURL, gitlabToken)
	if err != nil {
		return fmt.Errorf("failed to get jobs: %v", err)
	}

	var jobs []JobResponse
	if err := json.Unmarshal(body, &jobs); err != nil {
		return fmt.Errorf("failed to parse jobs: %v", err)
	}

	for _, job := range jobs {
		if job.Name == jobName && (job.Status == "manual" || job.Status == "scheduled") {
			playURL := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/play", gitlabURI, projectPath, job.ID)
			req, err := http.NewRequest("POST", playURL, nil)
			if err != nil {
				return fmt.Errorf("failed to create play request: %v", err)
			}
			req.Header.Set("PRIVATE-TOKEN", gitlabToken)

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("failed to trigger job: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
				return fmt.Errorf("failed to trigger job, status code: %d", resp.StatusCode)
			}

			fmt.Printf("  %s✓ Triggered job \"%s\" for %s%s\n", colorGreen, jobName, serviceName, colorReset)
			return nil
		}
	}

	return fmt.Errorf("job \"%s\" not found with manual/scheduled status", jobName)
}

// pollMainPipeline checks the main pipeline status and looks for a "deploy service" bridge.
// Returns (pollSuccess, 0, nil) if main pipeline succeeded without downstream.
// Returns (pollContinue, downstreamID, nil) if downstream pipeline found.
// Returns (pollContinue, 0, nil) if still in progress.
// Returns (pollContinue, 0, err) on terminal or transient errors.
func pollMainPipeline(client *http.Client, gitlabURI, gitlabToken, projectPath string, pipelineID int, serviceName string) (pollResult, int, error) {
	// Check main pipeline status
	pipelineURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d", gitlabURI, projectPath, pipelineID)
	body, err := gitlabGet(client, pipelineURL, gitlabToken)
	if err != nil {
		return pollContinue, 0, fmt.Errorf("failed to check pipeline for %s: %v", serviceName, err)
	}

	var pipelineResp PipelineResponse
	if err := json.Unmarshal(body, &pipelineResp); err != nil {
		return pollContinue, 0, fmt.Errorf("failed to parse pipeline response for %s: %v", serviceName, err)
	}

	// Check for bridges (downstream pipelines)
	bridgesURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/bridges", gitlabURI, projectPath, pipelineID)
	bridgesBody, err := gitlabGet(client, bridgesURL, gitlabToken)
	if err != nil {
		return pollContinue, 0, fmt.Errorf("failed to check bridges for %s: %v", serviceName, err)
	}

	var bridges []BridgeResponse
	if err := json.Unmarshal(bridgesBody, &bridges); err != nil {
		return pollContinue, 0, fmt.Errorf("failed to parse bridges response for %s: %v", serviceName, err)
	}

	for _, bridge := range bridges {
		if bridge.Name == "deploy service" && bridge.DownstreamPipeline != nil {
			fmt.Printf("  Found downstream pipeline %d for %s\n", bridge.DownstreamPipeline.ID, serviceName)
			return pollContinue, bridge.DownstreamPipeline.ID, nil
		}
	}

	// No downstream pipeline — check main pipeline status
	switch pipelineResp.Status {
	case "success":
		fmt.Printf("  %s✓ Pipeline completed successfully for %s%s\n", colorGreen, serviceName, colorReset)
		return pollSuccess, 0, nil
	case "failed", "canceled", "skipped":
		return pollContinue, 0, &terminalError{fmt.Sprintf("pipeline %s for %s", pipelineResp.Status, serviceName)}
	default:
		fmt.Printf("  Pipeline for %s is %s...\n", serviceName, pipelineResp.Status)
	}

	return pollContinue, 0, nil
}

// pollDeployHelmJob checks the "deploy helm" job status in the downstream pipeline.
func pollDeployHelmJob(client *http.Client, gitlabURI, gitlabToken, projectPath string, downstreamPipelineID int, serviceName string) (pollResult, error) {
	jobsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs", gitlabURI, projectPath, downstreamPipelineID)
	body, err := gitlabGet(client, jobsURL, gitlabToken)
	if err != nil {
		return pollContinue, fmt.Errorf("failed to check downstream jobs for %s: %v", serviceName, err)
	}

	var jobs []JobResponse
	if err := json.Unmarshal(body, &jobs); err != nil {
		return pollContinue, fmt.Errorf("failed to parse downstream jobs for %s: %v", serviceName, err)
	}

	for _, job := range jobs {
		if job.Name == "deploy helm" {
			switch job.Status {
			case "success":
				fmt.Printf("  %s✓ Job \"deploy helm\" completed successfully for %s%s\n", colorGreen, serviceName, colorReset)
				return pollSuccess, nil
			case "failed", "canceled":
				return pollContinue, &terminalError{fmt.Sprintf("job \"deploy helm\" %s for %s", job.Status, serviceName)}
			default:
				fmt.Printf("  Job \"deploy helm\" for %s is %s...\n", serviceName, job.Status)
			}
			return pollContinue, nil
		}
	}

	fmt.Printf("  Waiting for job \"deploy helm\" to appear in downstream pipeline for %s...\n", serviceName)
	return pollContinue, nil
}
