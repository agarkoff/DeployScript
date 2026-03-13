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

// JobResponse represents a GitLab pipeline job
type JobResponse struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Stage  string `json:"stage"`
	Status string `json:"status"`
}

// PipelineVariable represents a GitLab pipeline variable
type PipelineVariable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

const (
	colorBlue  = "\033[34m"
	colorGreen = "\033[32m"
	colorReset = "\033[0m"
)

// CreatePipelinesFromConfig creates GitLab pipelines for each namespace sequentially.
// ALL services must succeed on namespace N before namespace N+1 begins.
func CreatePipelinesFromConfig(cfg *config.Config, ref string, namespaces []string) error {
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	if gitlabToken == "" {
		return fmt.Errorf("GITLAB_TOKEN environment variable is not set")
	}

	gitlabURI := os.Getenv("GITLAB_URI")
	if gitlabURI == "" {
		return fmt.Errorf("GITLAB_URI environment variable is not set")
	}

	for _, namespace := range namespaces {
		fmt.Printf("\n%s=== Deploying to namespace: %s ===%s\n", colorBlue, namespace, colorReset)

		// Process sequential services first
		for _, service := range cfg.Sequential {
			fmt.Printf("\n%sStarting pipeline for sequential service: %s on tag: %s (namespace: %s)%s\n", colorBlue, service.Name, ref, namespace, colorReset)

			pipelineID, err := createPipelineForService(service, gitlabURI, gitlabToken, ref, namespace)
			if err != nil {
				return fmt.Errorf("failed to create pipeline for %s: %v", service.Name, err)
			}

			if err := waitForPipelineForService(service, gitlabURI, gitlabToken, pipelineID); err != nil {
				return fmt.Errorf("pipeline failed for %s: %v", service.Name, err)
			}
		}

		// Process each group in sequence, but services within a group in parallel
		for groupName, groupServices := range cfg.Groups {
			fmt.Printf("\n%sStarting pipelines for group: %s on tag: %s (namespace: %s)%s\n", colorBlue, groupName, ref, namespace, colorReset)

			var wg sync.WaitGroup
			errors := make(chan error, len(groupServices))

			for _, service := range groupServices {
				wg.Add(1)
				go func(svc config.Service) {
					defer wg.Done()

					pipelineID, err := createPipelineForService(svc, gitlabURI, gitlabToken, ref, namespace)
					if err != nil {
						errors <- fmt.Errorf("failed to create pipeline for %s: %v", svc.Name, err)
						return
					}

					if err := waitForPipelineForService(svc, gitlabURI, gitlabToken, pipelineID); err != nil {
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

		fmt.Printf("\n%s=== Namespace %s completed ===%s\n", colorGreen, namespace, colorReset)
	}

	return nil
}

// ContinuePipelinesFromConfig checks pipeline statuses and re-runs failed/missing ones
// for each namespace sequentially. Matches pipelines by ref + HELM_NAMESPACE variable.
func ContinuePipelinesFromConfig(cfg *config.Config, ref string, namespaces []string) error {
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	if gitlabToken == "" {
		return fmt.Errorf("GITLAB_TOKEN environment variable is not set")
	}

	gitlabURI := os.Getenv("GITLAB_URI")
	if gitlabURI == "" {
		return fmt.Errorf("GITLAB_URI environment variable is not set")
	}

	client := &http.Client{Timeout: 30 * time.Second}

	for _, namespace := range namespaces {
		fmt.Printf("\n%s=== Continuing deployment for namespace: %s ===%s\n", colorBlue, namespace, colorReset)

		continueService := func(service config.Service) error {
			info, err := checkServicePipelineStatus(client, gitlabURI, gitlabToken, service.GitlabProject, ref, service.Name, namespace)
			if err != nil {
				return fmt.Errorf("failed to check pipeline status for %s: %v", service.Name, err)
			}

			switch info.result {
			case pipelineSuccess:
				fmt.Printf("  %s✓ %s already deployed successfully (namespace: %s), skipping%s\n", colorGreen, service.Name, namespace, colorReset)
				return nil

			case pipelineRunning:
				fmt.Printf("  %sWaiting for existing pipeline %d for %s (namespace: %s)%s\n", colorBlue, info.pipelineID, service.Name, namespace, colorReset)
				return waitForPipelineForService(service, gitlabURI, gitlabToken, info.pipelineID)

			default: // pipelineNeedsRerun
				fmt.Printf("\n%sRe-running pipeline for %s on tag: %s (namespace: %s)%s\n", colorBlue, service.Name, ref, namespace, colorReset)
				pipelineID, err := createPipelineForService(service, gitlabURI, gitlabToken, ref, namespace)
				if err != nil {
					return fmt.Errorf("failed to create pipeline for %s: %v", service.Name, err)
				}
				return waitForPipelineForService(service, gitlabURI, gitlabToken, pipelineID)
			}
		}

		// Process sequential services first
		for _, service := range cfg.Sequential {
			if err := continueService(service); err != nil {
				return fmt.Errorf("pipeline failed for %s: %v", service.Name, err)
			}
		}

		// Process each group in sequence, but services within a group in parallel
		for groupName, groupServices := range cfg.Groups {
			fmt.Printf("\n%sProcessing group: %s (namespace: %s)%s\n", colorBlue, groupName, namespace, colorReset)

			var wg sync.WaitGroup
			errors := make(chan error, len(groupServices))

			for _, service := range groupServices {
				wg.Add(1)
				go func(svc config.Service) {
					defer wg.Done()
					if err := continueService(svc); err != nil {
						errors <- fmt.Errorf("pipeline failed for %s: %v", svc.Name, err)
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

		fmt.Printf("\n%s=== Namespace %s completed ===%s\n", colorGreen, namespace, colorReset)
	}

	return nil
}

// pipelineCheckResult represents the result of checking a service's pipeline status
type pipelineCheckResult int

const (
	pipelineNeedsRerun pipelineCheckResult = iota // no pipeline or failed — create new
	pipelineSuccess                               // already succeeded — skip
	pipelineRunning                               // in progress — wait for it
)

// pipelineCheckInfo holds the result of a pipeline status check
type pipelineCheckInfo struct {
	result     pipelineCheckResult
	pipelineID int // set when result is pipelineRunning
}

// checkServicePipelineStatus checks the latest pipeline status for a service,
// matching by ref and HELM_NAMESPACE pipeline variable.
func checkServicePipelineStatus(client *http.Client, gitlabURI, gitlabToken, gitlabProject, ref, serviceName, helmNamespace string) (pipelineCheckInfo, error) {
	projectPath := url.QueryEscape(gitlabProject)
	updatedAfter := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)

	// Get recent pipelines for this ref
	pipelinesURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines?ref=%s&updated_after=%s&order_by=id&sort=desc",
		gitlabURI, projectPath, url.QueryEscape(ref), url.QueryEscape(updatedAfter))

	body, err := gitlabGet(client, pipelinesURL, gitlabToken)
	if err != nil {
		return pipelineCheckInfo{result: pipelineNeedsRerun}, fmt.Errorf("failed to list pipelines: %v", err)
	}

	var pipelines []PipelineResponse
	if err := json.Unmarshal(body, &pipelines); err != nil {
		return pipelineCheckInfo{result: pipelineNeedsRerun}, fmt.Errorf("failed to parse pipelines: %v", err)
	}

	if len(pipelines) == 0 {
		fmt.Printf("  No pipelines found for %s on %s in last 24h\n", serviceName, ref)
		return pipelineCheckInfo{result: pipelineNeedsRerun}, nil
	}

	// Find pipeline matching HELM_NAMESPACE variable
	var runningInfo pipelineCheckInfo
	for _, pipeline := range pipelines {
		varsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/variables",
			gitlabURI, projectPath, pipeline.ID)

		varsBody, err := gitlabGet(client, varsURL, gitlabToken)
		if err != nil {
			fmt.Printf("  Warning: could not get variables for pipeline %d: %v\n", pipeline.ID, err)
			continue
		}

		var variables []PipelineVariable
		if err := json.Unmarshal(varsBody, &variables); err != nil {
			fmt.Printf("  Warning: could not parse variables for pipeline %d: %v\n", pipeline.ID, err)
			continue
		}

		// Check if HELM_NAMESPACE matches
		namespaceMatches := false
		for _, v := range variables {
			if v.Key == "HELM_NAMESPACE" && v.Value == helmNamespace {
				namespaceMatches = true
				break
			}
		}

		if !namespaceMatches {
			continue
		}

		// Found matching pipeline — check if all stages completed (success/warning)
		switch pipeline.Status {
		case "success", "warning":
			fmt.Printf("  Found successful pipeline %d for %s with HELM_NAMESPACE=%s (status: %s)\n", pipeline.ID, serviceName, helmNamespace, pipeline.Status)
			return pipelineCheckInfo{result: pipelineSuccess}, nil
		case "running", "pending", "created":
			// Remember the first running pipeline, but keep looking for a successful one
			if runningInfo.pipelineID == 0 {
				runningInfo = pipelineCheckInfo{result: pipelineRunning, pipelineID: pipeline.ID}
			}
		default:
			// failed/canceled — skip, check remaining pipelines
			fmt.Printf("  Pipeline %d for %s is %s, checking other pipelines...\n", pipeline.ID, serviceName, pipeline.Status)
		}
	}

	// No successful pipeline found — but maybe one is still running
	if runningInfo.pipelineID != 0 {
		fmt.Printf("  No successful pipeline found for %s, but pipeline %d is still running, waiting...\n", serviceName, runningInfo.pipelineID)
		return runningInfo, nil
	}

	// No matching pipelines at all, or all failed
	fmt.Printf("  No successful pipeline found for %s on %s with HELM_NAMESPACE=%s in last 24h\n", serviceName, ref, helmNamespace)
	return pipelineCheckInfo{result: pipelineNeedsRerun}, nil
}

// createPipelineForService creates a pipeline for config.Service
func createPipelineForService(service config.Service, gitlabURI, gitlabToken, ref, helmNamespace string) (int, error) {
	gitlabService := Service{
		Name:          service.Name,
		Directory:     service.Directory,
		GitlabProject: service.GitlabProject,
	}
	return createPipeline(gitlabService, gitlabURI, gitlabToken, ref, helmNamespace)
}

// waitForPipelineForService waits for a pipeline for config.Service
func waitForPipelineForService(service config.Service, gitlabURI, gitlabToken string, pipelineID int) error {
	gitlabService := Service{
		Name:          service.Name,
		Directory:     service.Directory,
		GitlabProject: service.GitlabProject,
	}
	return waitForPipeline(gitlabService, gitlabURI, gitlabToken, pipelineID)
}

// createPipeline creates a single pipeline with HELM_NAMESPACE variable
func createPipeline(service Service, gitlabURI, gitlabToken, ref, helmNamespace string) (int, error) {
	projectPath := url.QueryEscape(service.GitlabProject)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/pipeline", gitlabURI, projectPath)

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

// gitlabGet performs a GET request to GitLab API
func gitlabGet(client *http.Client, apiURL, token string) ([]byte, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitLab API returned %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// pollResult represents the outcome of a single polling iteration
type pollResult int

const (
	pollContinue pollResult = iota // keep polling
	pollSuccess                    // done successfully
)

// terminalError represents a non-retryable error (pipeline/job failed or canceled)
type terminalError struct {
	message string
}

func (e *terminalError) Error() string {
	return e.message
}

// waitForPipeline waits for a pipeline to complete by polling the pipeline status
// and the "deploy helm" job directly.
func waitForPipeline(service Service, gitlabURI, gitlabToken string, pipelineID int) error {
	projectPath := url.QueryEscape(service.GitlabProject)
	client := &http.Client{Timeout: 30 * time.Second}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	maxDuration := 60 * time.Minute
	maxRetryDuration := 60 * time.Minute
	var firstErrorTime time.Time

	for {
		result, err := pollPipeline(client, gitlabURI, gitlabToken, projectPath, pipelineID, service.Name)

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

// pollPipeline checks the pipeline status and "deploy helm" job directly.
// Returns pollSuccess when "deploy helm" succeeds.
// Returns terminalError when pipeline or "deploy helm" job fails/cancels.
// Returns pollContinue to keep polling.
func pollPipeline(client *http.Client, gitlabURI, gitlabToken, projectPath string, pipelineID int, serviceName string) (pollResult, error) {
	// Check pipeline status
	pipelineURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d", gitlabURI, projectPath, pipelineID)
	body, err := gitlabGet(client, pipelineURL, gitlabToken)
	if err != nil {
		return pollContinue, fmt.Errorf("failed to check pipeline for %s: %v", serviceName, err)
	}

	var pipelineResp PipelineResponse
	if err := json.Unmarshal(body, &pipelineResp); err != nil {
		return pollContinue, fmt.Errorf("failed to parse pipeline response for %s: %v", serviceName, err)
	}

	// Terminal pipeline states
	if pipelineResp.Status == "failed" || pipelineResp.Status == "canceled" {
		return pollContinue, &terminalError{fmt.Sprintf("pipeline %s for %s", pipelineResp.Status, serviceName)}
	}

	// Check "deploy helm" job
	jobsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs?per_page=100", gitlabURI, projectPath, pipelineID)
	jobsBody, err := gitlabGet(client, jobsURL, gitlabToken)
	if err != nil {
		return pollContinue, fmt.Errorf("failed to check jobs for %s: %v", serviceName, err)
	}

	var jobs []JobResponse
	if err := json.Unmarshal(jobsBody, &jobs); err != nil {
		return pollContinue, fmt.Errorf("failed to parse jobs for %s: %v", serviceName, err)
	}

	// Check "deploy helm" job first
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
				return pollContinue, nil
			}
		}
	}

	// No "deploy helm" job — fall back to checking "deploy" stage jobs
	result, termErr := pollDeployStage(jobs, serviceName)
	if result == pollSuccess || termErr != nil {
		return result, termErr
	}

	// No deploy stage jobs found yet
	fmt.Printf("  Pipeline for %s is %s, waiting for deploy jobs...\n", serviceName, pipelineResp.Status)
	return pollContinue, nil
}

// checkDeployStageStatus checks all jobs in the "deploy" stage.
// Returns (result, true) if deploy stage jobs were found, (_, false) otherwise.
// Success = all jobs are "success" or "warning". Any failed/canceled = needsRerun.
// Any running/pending/created = running.
func checkDeployStageStatus(jobs []JobResponse, pipelineID int, serviceName string) (pipelineCheckInfo, bool) {
	var deployJobs []JobResponse
	for _, job := range jobs {
		if job.Stage == "deploy" {
			deployJobs = append(deployJobs, job)
		}
	}

	if len(deployJobs) == 0 {
		return pipelineCheckInfo{}, false
	}

	allDone := true
	for _, job := range deployJobs {
		switch job.Status {
		case "success", "warning":
			// ok
		case "failed", "canceled":
			fmt.Printf("  Pipeline %d for %s: deploy stage job \"%s\" is %s\n", pipelineID, serviceName, job.Name, job.Status)
			return pipelineCheckInfo{result: pipelineNeedsRerun}, true
		default:
			allDone = false
		}
	}

	if allDone {
		fmt.Printf("  Pipeline %d for %s: all deploy stage jobs completed successfully\n", pipelineID, serviceName)
		return pipelineCheckInfo{result: pipelineSuccess}, true
	}

	fmt.Printf("  Pipeline %d for %s: deploy stage jobs still running, waiting...\n", pipelineID, serviceName)
	return pipelineCheckInfo{result: pipelineRunning, pipelineID: pipelineID}, true
}

// pollDeployStage checks all jobs in the "deploy" stage during polling.
// Returns (pollSuccess, nil) if all done, (pollContinue, terminalError) on failure,
// (pollContinue, nil) if still running or no deploy jobs found.
func pollDeployStage(jobs []JobResponse, serviceName string) (pollResult, error) {
	var deployJobs []JobResponse
	for _, job := range jobs {
		if job.Stage == "deploy" {
			deployJobs = append(deployJobs, job)
		}
	}

	if len(deployJobs) == 0 {
		return pollContinue, nil
	}

	allDone := true
	for _, job := range deployJobs {
		switch job.Status {
		case "success", "warning":
			// ok
		case "failed", "canceled":
			return pollContinue, &terminalError{fmt.Sprintf("deploy stage job \"%s\" %s for %s", job.Name, job.Status, serviceName)}
		default:
			allDone = false
		}
	}

	if allDone {
		fmt.Printf("  %s✓ All deploy stage jobs completed successfully for %s%s\n", colorGreen, serviceName, colorReset)
		return pollSuccess, nil
	}

	fmt.Printf("  Deploy stage jobs for %s still running...\n", serviceName)
	return pollContinue, nil
}
