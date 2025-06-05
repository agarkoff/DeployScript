package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"deploy/config"
	"deploy/git"
	"deploy/gitlab"
	"deploy/maven"
)

func main() {
	// Parse command line arguments
	var helmNamespace string
	flag.StringVar(&helmNamespace, "namespace", "", "Helm namespace to use if not set in GitLab")
	flag.Parse()
	args := flag.Args()

	if len(args) != 2 {
		log.Fatal("Usage: deploy [-namespace <namespace>] <directory> <version>")
	}

	baseDir := args[0]
	version := args[1]

	// Read configuration file
	cfg, err := config.ReadYAMLConfig("deploy.yaml")
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}

	// Build service directories map
	serviceDirs := make(map[string]string)
	serviceConfigs := make(map[string]gitlab.Service)

	for _, service := range cfg.Services {
		serviceDir := filepath.Join(baseDir, service.Directory)

		// Check if service directory exists
		if _, err := os.Stat(serviceDir); os.IsNotExist(err) {
			log.Fatalf("Service directory does not exist: %s", serviceDir)
		}

		serviceDirs[service.Name] = serviceDir

		// Convert config.Service to gitlab.Service
		gitlabService := gitlab.Service{
			Name:          service.Name,
			Directory:     service.Directory,
			GitlabProject: service.GitlabProject,
			Group:         service.Group,
			Sequential:    service.Sequential,
		}
		serviceConfigs[service.Name] = gitlabService
	}

	// Extract service names for compatibility
	services := make([]string, len(cfg.Services))
	for i, service := range cfg.Services {
		services[i] = service.Name
	}

	// Phase 1: Check if all git working copies are clean
	fmt.Println("Phase 1: Checking git status...")
	for _, service := range services {
		fmt.Printf("  Checking service: %s\n", service)
		if err := git.CheckClean(serviceDirs[service]); err != nil {
			fmt.Printf("\nWarning: Git working copy is not clean in %s\n", service)

			// Show git status
			if err := git.ShowStatus(serviceDirs[service]); err != nil {
				log.Fatalf("Failed to show git status in %s: %v", service, err)
			}

			// Ask user if they want to clean
			fmt.Printf("\nDo you want to clean the working directory for %s? (y/n): ", service)
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			response = strings.TrimSpace(strings.ToLower(response))

			if response != "y" && response != "yes" {
				log.Fatal("Deployment cancelled by user")
			}

			// Clean the working directory
			fmt.Printf("  Cleaning working directory for %s...\n", service)
			if err := git.CleanWorkingDirectory(serviceDirs[service]); err != nil {
				log.Fatalf("Failed to clean working directory in %s: %v", service, err)
			}
		}
	}

	// Phase 2: Switch all to develop branch
	fmt.Println("\nPhase 2: Switching to develop branch...")
	for _, service := range services {
		fmt.Printf("  Switching service: %s\n", service)
		if err := git.Checkout(serviceDirs[service], "develop"); err != nil {
			log.Fatalf("Failed to checkout develop branch in %s: %v", service, err)
		}
	}

	// Phase 3: Pull latest changes for all
	fmt.Println("\nPhase 3: Pulling latest changes...")
	for _, service := range services {
		fmt.Printf("  Pulling service: %s\n", service)
		if err := git.Pull(serviceDirs[service]); err != nil {
			log.Fatalf("Failed to pull in %s: %v", service, err)
		}
	}

	// Phase 4: Update all pom.xml files
	fmt.Println("\nPhase 4: Updating pom.xml files...")
	for _, service := range services {
		fmt.Printf("  Updating service: %s\n", service)
		if err := maven.UpdatePomFiles(serviceDirs[service], version); err != nil {
			log.Fatalf("Failed to update pom files in %s: %v", service, err)
		}
	}

	// Phase 5: Create release branches for all
	fmt.Println("\nPhase 5: Creating release branches...")
	branchName := fmt.Sprintf("release/%s", version)
	for _, service := range services {
		fmt.Printf("  Creating branch for service: %s\n", service)

		// Delete branch if it already exists (locally and remotely)
		if err := git.DeleteBranchIfExists(serviceDirs[service], branchName); err != nil {
			log.Fatalf("Failed to delete existing branch in %s: %v", service, err)
		}

		// Create new branch
		if err := git.Checkout(serviceDirs[service], "-b", branchName); err != nil {
			log.Fatalf("Failed to create release branch in %s: %v", service, err)
		}
	}

	// Show all diffs before committing
	fmt.Println("\nShowing all changes before commit:")
	fmt.Println(strings.Repeat("=", 80))
	for _, service := range services {
		fmt.Printf("\n--- Changes in service: %s ---\n", service)
		if err := git.ShowDiff(serviceDirs[service]); err != nil {
			// Don't fail if diff is empty, just continue
			fmt.Println("No changes to show")
		}
	}
	fmt.Println(strings.Repeat("=", 80))

	// Phase 6: Commit changes for all
	fmt.Println("\nPhase 6: Committing changes...")
	commitMsg := fmt.Sprintf("Up to version %s.0", version)
	for _, service := range services {
		fmt.Printf("  Committing service: %s\n", service)
		if err := git.AddAll(serviceDirs[service]); err != nil {
			log.Fatalf("Failed to add files in %s: %v", service, err)
		}
		if err := git.Commit(serviceDirs[service], commitMsg); err != nil {
			log.Fatalf("Failed to commit in %s: %v", service, err)
		}
	}

	// Phase 7: Create tags for all
	fmt.Println("\nPhase 7: Creating tags...")
	tagName := fmt.Sprintf("release/%s.0", version)
	for _, service := range services {
		fmt.Printf("  Creating tag for service: %s\n", service)

		// Delete tag if it already exists (locally and remotely)
		if err := git.DeleteTagIfExists(serviceDirs[service], tagName); err != nil {
			log.Fatalf("Failed to delete existing tag in %s: %v", service, err)
		}

		// Create new tag
		if err := git.Tag(serviceDirs[service], tagName); err != nil {
			log.Fatalf("Failed to create tag in %s: %v", service, err)
		}
	}

	// Phase 8: Clean Maven cache and build all services
	fmt.Println("\nPhase 8: Cleaning Maven cache and building services...")

	// Clean Maven cache
	if err := maven.CleanCache(); err != nil {
		log.Fatalf("Failed to clean Maven cache: %v", err)
	}

	// Build all services in order
	for _, service := range services {
		fmt.Printf("\nBuilding service: %s\n", service)
		fmt.Println(strings.Repeat("-", 60))

		if err := maven.BuildService(serviceDirs[service]); err != nil {
			log.Fatalf("Build failed for service %s: %v", service, err)
		}

		fmt.Printf("%sService %s built successfully!%s\n", git.ColorGreen, service, git.ColorReset)
	}

	// Wait for user confirmation
	fmt.Println("\nAll services built successfully!")
	fmt.Println("Press Enter to continue and push changes...")
	reader := bufio.NewReader(os.Stdin)
	reader.ReadString('\n')

	// Phase 9: Push changes and tags for all
	fmt.Println("\nPhase 9: Pushing changes and tags...")
	for _, service := range services {
		fmt.Printf("  Pushing service: %s\n", service)
		if err := git.PushWithTags(serviceDirs[service]); err != nil {
			log.Fatalf("Failed to push in %s: %v", service, err)
		}
	}

	// Phase 10: Create GitLab pipelines
	fmt.Println("\nPhase 10: Creating GitLab pipelines...")

	// Convert services to gitlab.Service slice
	gitlabServices := make([]gitlab.Service, len(cfg.Services))
	for i, service := range cfg.Services {
		gitlabServices[i] = gitlab.Service{
			Name:          service.Name,
			Directory:     service.Directory,
			GitlabProject: service.GitlabProject,
			Group:         service.Group,
			Sequential:    service.Sequential,
		}
	}

	if err := gitlab.CreatePipelines(gitlabServices, branchName, helmNamespace); err != nil {
		log.Fatalf("Failed to create GitLab pipelines: %v", err)
	}

	fmt.Println("\nDeployment script completed successfully!")
}
