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
	"sort"
	"strings"
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
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Stage        string `json:"stage"`
	Status       string `json:"status"`
	AllowFailure bool   `json:"allow_failure"`
}

// isJobFailed returns true if a job has failed and is NOT allowed to fail
func isJobFailed(job JobResponse) bool {
	return (job.Status == "failed" || job.Status == "canceled") && !job.AllowFailure
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

// CreatePipelinesFromConfig creates GitLab pipelines using a pipelined approach:
// as soon as a service succeeds on namespace N, it starts on namespace N+1,
// without waiting for other services to finish on namespace N.
// Within a namespace, ordering is preserved: sequential services first, then groups in order.
func CreatePipelinesFromConfig(cfg *config.Config, ref string, namespaces []string) error {
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	if gitlabToken == "" {
		return fmt.Errorf("GITLAB_TOKEN environment variable is not set")
	}

	gitlabURI := os.Getenv("GITLAB_URI")
	if gitlabURI == "" {
		return fmt.Errorf("GITLAB_URI environment variable is not set")
	}

	// Build deployment phases: each sequential service is its own phase,
	// each group is a phase with parallel services.
	type deployPhase struct {
		services []config.Service
	}

	var phases []deployPhase
	for _, svc := range cfg.Sequential {
		phases = append(phases, deployPhase{services: []config.Service{svc}})
	}

	var groupNames []string
	for name := range cfg.Groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		phases = append(phases, deployPhase{services: cfg.Groups[name]})
	}

	numPhases := len(phases)
	numNS := len(namespaces)

	// Synchronization channels:
	// phaseDone[p][n] — closed when ALL services in phase p complete on namespace n
	// svcDone[p][s][n] — closed when service s in phase p completes on namespace n
	phaseDone := make([][]chan struct{}, numPhases)
	svcDone := make([][][]chan struct{}, numPhases)
	for p := 0; p < numPhases; p++ {
		phaseDone[p] = make([]chan struct{}, numNS)
		svcDone[p] = make([][]chan struct{}, len(phases[p].services))
		for n := 0; n < numNS; n++ {
			phaseDone[p][n] = make(chan struct{})
		}
		for s := 0; s < len(phases[p].services); s++ {
			svcDone[p][s] = make([]chan struct{}, numNS)
			for n := 0; n < numNS; n++ {
				svcDone[p][s][n] = make(chan struct{})
			}
		}
	}

	var mu sync.Mutex
	var allErrors []string
	var wg sync.WaitGroup

	// Phase completion monitors: close phaseDone[p][n] when all services in phase finish
	for p := 0; p < numPhases; p++ {
		for n := 0; n < numNS; n++ {
			wg.Add(1)
			go func(p, n int) {
				defer wg.Done()
				for s := 0; s < len(phases[p].services); s++ {
					<-svcDone[p][s][n]
				}
				close(phaseDone[p][n])
			}(p, n)
		}
	}

	// Service goroutines: each service pipelines through namespaces
	for p := 0; p < numPhases; p++ {
		for s, svc := range phases[p].services {
			wg.Add(1)
			go func(p, s int, svc config.Service) {
				defer wg.Done()
				svcFailed := false

				for n := 0; n < numNS; n++ {
					namespace := namespaces[n]

					// Library services deploy only to first namespace
					if svc.IsLibrary && n > 0 {
						fmt.Printf("  Skipping library service %s on %s (only first namespace)\n", svc.Name, namespace)
						close(svcDone[p][s][n])
						continue
					}

					// If service failed on a previous namespace, skip remaining
					if svcFailed {
						close(svcDone[p][s][n])
						continue
					}

					// Wait for previous phase to finish on this namespace
					if p > 0 {
						<-phaseDone[p-1][n]
					}
					// Wait for this service to finish on previous namespace
					if n > 0 {
						<-svcDone[p][s][n-1]
					}

					fmt.Printf("\n%sStarting pipeline for %s on tag: %s (namespace: %s)%s\n", colorBlue, svc.Name, ref, namespace, colorReset)

					pipelineID, err := createPipelineForService(svc, gitlabURI, gitlabToken, ref, namespace)
					if err != nil {
						errMsg := fmt.Sprintf("failed to create pipeline for %s (namespace: %s): %v", svc.Name, namespace, err)
						fmt.Printf("  \033[31m✗ %s\033[0m\n", errMsg)
						mu.Lock()
						allErrors = append(allErrors, errMsg)
						mu.Unlock()
						svcFailed = true
						close(svcDone[p][s][n])
						continue
					}

					if err := waitForPipelineForService(svc, gitlabURI, gitlabToken, pipelineID, namespace); err != nil {
						errMsg := fmt.Sprintf("pipeline failed for %s (namespace: %s): %v", svc.Name, namespace, err)
						fmt.Printf("  \033[31m✗ %s\033[0m\n", errMsg)
						mu.Lock()
						allErrors = append(allErrors, errMsg)
						mu.Unlock()
						svcFailed = true
						close(svcDone[p][s][n])
						continue
					}

					close(svcDone[p][s][n])
				}
			}(p, s, svc)
		}
	}

	wg.Wait()

	if len(allErrors) > 0 {
		fmt.Printf("\n\033[31m=== Failed pipelines ===\033[0m\n")
		for _, e := range allErrors {
			fmt.Printf("  \033[31m✗ %s\033[0m\n", e)
		}
		return fmt.Errorf("%d pipeline(s) failed", len(allErrors))
	}

	fmt.Printf("\n%s=== All namespaces deployed successfully ===%s\n", colorGreen, colorReset)
	return nil
}

// ContinuePipelinesFromConfig checks pipeline statuses and re-runs failed/missing ones.
// All namespaces are processed in parallel since continue mode recovers an existing deployment.
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

	var mu sync.Mutex
	var allErrors []string

	var nsWg sync.WaitGroup
	for i, namespace := range namespaces {
		nsWg.Add(1)
		go func(i int, namespace string) {
			defer nsWg.Done()
			errs := continueNamespace(cfg, client, gitlabURI, gitlabToken, ref, namespace, i == 0)
			if len(errs) > 0 {
				mu.Lock()
				allErrors = append(allErrors, errs...)
				mu.Unlock()
			}
		}(i, namespace)
	}
	nsWg.Wait()

	if len(allErrors) > 0 {
		fmt.Printf("\n\033[31m=== Failed pipelines ===\033[0m\n")
		for _, e := range allErrors {
			fmt.Printf("  \033[31m✗ %s\033[0m\n", e)
		}
		return fmt.Errorf("%d pipeline(s) failed across namespaces", len(allErrors))
	}

	return nil
}

// continueNamespace processes a single namespace in continue mode.
// Returns a list of error messages for failed services.
func continueNamespace(cfg *config.Config, client *http.Client, gitlabURI, gitlabToken, ref, namespace string, isFirstNamespace bool) []string {
	fmt.Printf("\n%s=== Continuing deployment for namespace: %s ===%s\n", colorBlue, namespace, colorReset)

	var errors []string

	continueService := func(service config.Service) error {
		info, err := checkServicePipelineStatus(client, gitlabURI, gitlabToken, service.GitlabProject, ref, service.Name, namespace)
		if err != nil {
			return fmt.Errorf("failed to check pipeline status for %s: %v", service.Name, err)
		}

		switch info.result {
		case pipelineSuccess:
			fmt.Printf("  %s✓ %s already deployed successfully (namespace: %s), skipping%s\n", colorGreen, service.Name, namespace, colorReset)
			if info.webURL != "" {
				fmt.Printf("    %s\n", info.webURL)
			}
			return nil

		case pipelineRunning:
			fmt.Printf("  %sWaiting for existing pipeline %d for %s (namespace: %s)%s\n", colorBlue, info.pipelineID, service.Name, namespace, colorReset)
			if info.webURL != "" {
				fmt.Printf("    %s\n", info.webURL)
			}
			return waitForPipelineForService(service, gitlabURI, gitlabToken, info.pipelineID, namespace)

		default: // pipelineNeedsRerun
			fmt.Printf("\n%sRe-running pipeline for %s on tag: %s (namespace: %s)%s\n", colorBlue, service.Name, ref, namespace, colorReset)
			pipelineID, err := createPipelineForService(service, gitlabURI, gitlabToken, ref, namespace)
			if err != nil {
				return fmt.Errorf("failed to create pipeline for %s: %v", service.Name, err)
			}
			return waitForPipelineForService(service, gitlabURI, gitlabToken, pipelineID, namespace)
		}
	}

	// Process sequential services first
	for _, service := range cfg.Sequential {
		if service.IsLibrary && !isFirstNamespace {
			fmt.Printf("  Skipping library service %s (only deployed to first namespace)\n", service.Name)
			continue
		}
		if err := continueService(service); err != nil {
			errMsg := fmt.Sprintf("[%s] %s: %v", namespace, service.Name, err)
			fmt.Printf("  \033[31m✗ %s\033[0m\n", errMsg)
			errors = append(errors, errMsg)
		}
	}

	// Process each group in sequence, but services within a group in parallel
	for groupName, groupServices := range cfg.Groups {
		var servicesToRun []config.Service
		for _, svc := range groupServices {
			if svc.IsLibrary && !isFirstNamespace {
				fmt.Printf("  Skipping library service %s (only deployed to first namespace)\n", svc.Name)
				continue
			}
			servicesToRun = append(servicesToRun, svc)
		}

		if len(servicesToRun) == 0 {
			continue
		}

		fmt.Printf("\n%sProcessing group: %s (namespace: %s)%s\n", colorBlue, groupName, namespace, colorReset)

		var wg sync.WaitGroup
		groupErrors := make(chan error, len(servicesToRun))

		for _, service := range servicesToRun {
			wg.Add(1)
			go func(svc config.Service) {
				defer wg.Done()
				if err := continueService(svc); err != nil {
					groupErrors <- fmt.Errorf("%s: %v", svc.Name, err)
				}
			}(service)
		}

		wg.Wait()
		close(groupErrors)

		for err := range groupErrors {
			if err != nil {
				errMsg := fmt.Sprintf("[%s] %v", namespace, err)
				fmt.Printf("  \033[31m✗ %s\033[0m\n", errMsg)
				errors = append(errors, errMsg)
			}
		}
	}

	if len(errors) > 0 {
		fmt.Printf("\n\033[31m=== Namespace %s completed with errors ===\033[0m\n", namespace)
	} else {
		fmt.Printf("\n%s=== Namespace %s completed ===%s\n", colorGreen, namespace, colorReset)
	}

	return errors
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
	pipelineID int    // set when result is pipelineRunning
	webURL     string // set when result is pipelineSuccess
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
			return pipelineCheckInfo{result: pipelineSuccess, webURL: pipeline.WebURL}, nil
		case "running", "pending", "created":
			// Check deploy jobs before assuming pipeline is still viable
			jobsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs?per_page=100",
				gitlabURI, projectPath, pipeline.ID)
			jobsBody, jobsErr := gitlabGet(client, jobsURL, gitlabToken)
			if jobsErr == nil {
				var jobs []JobResponse
				if json.Unmarshal(jobsBody, &jobs) == nil {
					// Check if "deploy helm" job is skipped/failed/canceled
					deploySkipped := false
					for _, job := range jobs {
						if job.Name == "deploy helm" {
							if job.Status == "skipped" || isJobFailed(job) {
								deploySkipped = true
								fmt.Printf("  Pipeline %d for %s: deploy helm job is %s, treating as failed\n", pipeline.ID, serviceName, job.Status)
							}
							break
						}
					}
					if deploySkipped {
						break // treat as failed, check next pipeline
					}
					// Also check deploy stage jobs via existing helper
					if info, found := checkDeployStageStatus(jobs, pipeline.ID, serviceName); found && info.result == pipelineNeedsRerun {
						break // deploy stage has failed/skipped jobs
					}
				}
			}
			// Remember the first running pipeline, but keep looking for a successful one
			if runningInfo.pipelineID == 0 {
				runningInfo = pipelineCheckInfo{result: pipelineRunning, pipelineID: pipeline.ID, webURL: pipeline.WebURL}
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
func waitForPipelineForService(service config.Service, gitlabURI, gitlabToken string, pipelineID int, namespace string) error {
	gitlabService := Service{
		Name:          service.Name,
		Directory:     service.Directory,
		GitlabProject: service.GitlabProject,
	}
	return waitForPipeline(gitlabService, gitlabURI, gitlabToken, pipelineID, namespace)
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

	// Cancel any test jobs immediately so they don't hold up the deploy stage
	jobsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs?per_page=100", gitlabURI, projectPath, pipelineResp.ID)
	if jobsBody, jobsErr := gitlabGet(client, jobsURL, gitlabToken); jobsErr == nil {
		var jobs []JobResponse
		if json.Unmarshal(jobsBody, &jobs) == nil {
			cancelTestJobs(client, gitlabURI, gitlabToken, projectPath, jobs, service.Name, helmNamespace)
		}
	}

	return pipelineResp.ID, nil
}

// cancelTestJobs cancels any job whose name contains "test" (case-insensitive)
// and has not finished yet. Test jobs are skipped during deployment so the
// pipeline can proceed straight to the deploy stage.
func cancelTestJobs(client *http.Client, gitlabURI, gitlabToken, projectPath string, jobs []JobResponse, serviceName, namespace string) {
	for _, job := range jobs {
		if !strings.Contains(strings.ToLower(job.Name), "test") {
			continue
		}
		switch job.Status {
		case "success", "failed", "canceled", "skipped":
			continue
		}
		cancelURL := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/cancel", gitlabURI, projectPath, job.ID)
		if err := gitlabPost(client, cancelURL, gitlabToken); err != nil {
			fmt.Printf("  Warning: failed to cancel test job %q for %s (%s): %v\n", job.Name, serviceName, namespace, err)
			continue
		}
		fmt.Printf("  Canceled test job %q for %s (%s)\n", job.Name, serviceName, namespace)
	}
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

// gitlabPost performs a POST request to GitLab API with no body.
func gitlabPost(client *http.Client, apiURL, token string) error {
	req, err := http.NewRequest("POST", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitLab API returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
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
func waitForPipeline(service Service, gitlabURI, gitlabToken string, pipelineID int, namespace string) error {
	projectPath := url.QueryEscape(service.GitlabProject)
	client := &http.Client{Timeout: 30 * time.Second}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	maxDuration := 60 * time.Minute
	maxRetryDuration := 60 * time.Minute
	var firstErrorTime time.Time

	for {
		result, err := pollPipeline(client, gitlabURI, gitlabToken, projectPath, pipelineID, service.Name, namespace)

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
func pollPipeline(client *http.Client, gitlabURI, gitlabToken, projectPath string, pipelineID int, serviceName, namespace string) (pollResult, error) {
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

	// Get jobs first — deploy helm success takes priority over pipeline-level status,
	// because non-critical jobs (e.g. "notify deploy") may fail the pipeline
	// even though the actual deployment succeeded.
	jobsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs?per_page=100", gitlabURI, projectPath, pipelineID)
	jobsBody, err := gitlabGet(client, jobsURL, gitlabToken)
	if err != nil {
		return pollContinue, fmt.Errorf("failed to check jobs for %s: %v", serviceName, err)
	}

	var jobs []JobResponse
	if err := json.Unmarshal(jobsBody, &jobs); err != nil {
		return pollContinue, fmt.Errorf("failed to parse jobs for %s: %v", serviceName, err)
	}

	// Cancel any test jobs that may have appeared since the last poll
	cancelTestJobs(client, gitlabURI, gitlabToken, projectPath, jobs, serviceName, namespace)

	pipelineFailed := pipelineResp.Status == "failed" || pipelineResp.Status == "canceled"

	// Check "deploy helm" job first
	for _, job := range jobs {
		if job.Name == "deploy helm" {
			switch job.Status {
			case "success":
				fmt.Printf("  %s✓ Job \"deploy helm\" completed successfully for %s (%s)%s\n", colorGreen, serviceName, namespace, colorReset)
				return pollSuccess, nil
			case "failed", "canceled", "skipped":
				return pollContinue, &terminalError{fmt.Sprintf("job \"deploy helm\" %s for %s (%s)", job.Status, serviceName, namespace)}
			case "created", "waiting_for_resource", "pending":
				// deploy helm hasn't started — if pipeline already failed, it will never start
				if pipelineFailed || hasFailedJobs(jobs, job.Stage) {
					return pollContinue, &terminalError{fmt.Sprintf("job \"deploy helm\" is %s but earlier jobs have failed for %s (%s)", job.Status, serviceName, namespace)}
				}
				fmt.Printf("  Job \"deploy helm\" for %s (%s) is %s...\n", serviceName, namespace, job.Status)
				return pollContinue, nil
			default:
				fmt.Printf("  Job \"deploy helm\" for %s (%s) is %s...\n", serviceName, namespace, job.Status)
				return pollContinue, nil
			}
		}
	}

	// No "deploy helm" job — fall back to checking "deploy" stage jobs
	result, termErr := pollDeployStage(jobs, serviceName, namespace)
	if result == pollSuccess || termErr != nil {
		return result, termErr
	}

	// Pipeline failed but no deploy jobs found at all
	if pipelineFailed {
		return pollContinue, &terminalError{fmt.Sprintf("pipeline %s for %s (%s, no deploy jobs found)", pipelineResp.Status, serviceName, namespace)}
	}

	// No deploy stage jobs found yet
	fmt.Printf("  Pipeline for %s (%s) is %s, waiting for deploy jobs...\n", serviceName, namespace, pipelineResp.Status)
	return pollContinue, nil
}

// checkDeployStageStatus checks all jobs in the "deploy" stage.
// Returns (result, true) if deploy stage jobs were found, (_, false) otherwise.
// Success = all jobs are "success" or "warning". Any failed/canceled = needsRerun.
// Any running/pending/created = running.
func checkDeployStageStatus(jobs []JobResponse, pipelineID int, serviceName string) (pipelineCheckInfo, bool) {
	var deployJobs []JobResponse
	for _, job := range jobs {
		if job.Stage == "deploy" && !ignoredJobs[job.Name] {
			deployJobs = append(deployJobs, job)
		}
	}

	if len(deployJobs) == 0 {
		return pipelineCheckInfo{}, false
	}

	allDone := true
	for _, job := range deployJobs {
		if job.Status == "success" || job.Status == "warning" || (job.Status == "failed" && job.AllowFailure) {
			continue // ok or allowed to fail
		}
		if job.Status == "failed" || job.Status == "canceled" || job.Status == "skipped" {
			fmt.Printf("  Pipeline %d for %s: deploy stage job \"%s\" is %s\n", pipelineID, serviceName, job.Name, job.Status)
			return pipelineCheckInfo{result: pipelineNeedsRerun}, true
		}
		allDone = false
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
func pollDeployStage(jobs []JobResponse, serviceName, namespace string) (pollResult, error) {
	var deployJobs []JobResponse
	for _, job := range jobs {
		if job.Stage == "deploy" && !ignoredJobs[job.Name] {
			deployJobs = append(deployJobs, job)
		}
	}

	if len(deployJobs) == 0 {
		return pollContinue, nil
	}

	allDone := true
	for _, job := range deployJobs {
		if job.Status == "success" || job.Status == "warning" || (job.Status == "failed" && job.AllowFailure) {
			continue // ok or allowed to fail
		}
		if job.Status == "failed" || job.Status == "canceled" || job.Status == "skipped" {
			return pollContinue, &terminalError{fmt.Sprintf("deploy stage job \"%s\" %s for %s (%s)", job.Name, job.Status, serviceName, namespace)}
		}
		allDone = false
	}

	if allDone {
		fmt.Printf("  %s✓ All deploy stage jobs completed successfully for %s (%s)%s\n", colorGreen, serviceName, namespace, colorReset)
		return pollSuccess, nil
	}

	fmt.Printf("  Deploy stage jobs for %s (%s) still running...\n", serviceName, namespace)
	return pollContinue, nil
}

// ignoredJobs contains job names that should not affect deployment logic
var ignoredJobs = map[string]bool{
	"notify deploy": true,
}

// hasFailedJobs checks if any job in stages before targetStage has failed or been canceled.
// This detects situations where the deploy job will never start because a prerequisite failed.
func hasFailedJobs(jobs []JobResponse, targetStage string) bool {
	for _, job := range jobs {
		if job.Stage == targetStage || ignoredJobs[job.Name] {
			continue
		}
		if isJobFailed(job) {
			return true
		}
	}
	return false
}
