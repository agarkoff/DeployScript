package main

import (
	"bufio"
	"encoding/xml"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// POM represents Maven pom.xml structure
type POM struct {
	XMLName    xml.Name    `xml:"project"`
	Version    string      `xml:"version"`
	Properties *Properties `xml:"properties,omitempty"`
	Modules    []string    `xml:"modules>module,omitempty"`
}

// Properties represents Maven properties
type Properties struct {
	Inner []byte `xml:",innerxml"`
}

func main() {
	// Parse command line arguments
	flag.Parse()
	args := flag.Args()

	if len(args) != 2 {
		log.Fatal("Usage: deploy <directory> <version>")
	}

	baseDir := args[0]
	version := args[1]

	// Read configuration file
	services, err := readConfig("deploy.conf")
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}

	// Build service directories map
	serviceDirs := make(map[string]string)
	for _, service := range services {
		serviceDir := filepath.Join(baseDir, service)

		// Check if service directory exists
		if _, err := os.Stat(serviceDir); os.IsNotExist(err) {
			log.Fatalf("Service directory does not exist: %s", serviceDir)
		}

		serviceDirs[service] = serviceDir
	}

	// Phase 1: Check if all git working copies are clean
	fmt.Println("Phase 1: Checking git status...")
	for _, service := range services {
		fmt.Printf("  Checking service: %s\n", service)
		if err := checkGitClean(serviceDirs[service]); err != nil {
			log.Fatalf("Git working copy is not clean in %s: %v", service, err)
		}
	}

	// Phase 2: Switch all to develop branch
	fmt.Println("\nPhase 2: Switching to develop branch...")
	for _, service := range services {
		fmt.Printf("  Switching service: %s\n", service)
		if err := gitCheckout(serviceDirs[service], "develop"); err != nil {
			log.Fatalf("Failed to checkout develop branch in %s: %v", service, err)
		}
	}

	// Phase 3: Pull latest changes for all
	fmt.Println("\nPhase 3: Pulling latest changes...")
	for _, service := range services {
		fmt.Printf("  Pulling service: %s\n", service)
		if err := gitPull(serviceDirs[service]); err != nil {
			log.Fatalf("Failed to pull in %s: %v", service, err)
		}
	}

	// Phase 4: Update all pom.xml files
	fmt.Println("\nPhase 4: Updating pom.xml files...")
	for _, service := range services {
		fmt.Printf("  Updating service: %s\n", service)
		if err := updatePomFiles(serviceDirs[service], version); err != nil {
			log.Fatalf("Failed to update pom files in %s: %v", service, err)
		}
	}

	// Phase 5: Create release branches for all
	fmt.Println("\nPhase 5: Creating release branches...")
	branchName := fmt.Sprintf("release/%s", version)
	for _, service := range services {
		fmt.Printf("  Creating branch for service: %s\n", service)
		if err := gitCheckout(serviceDirs[service], "-b", branchName); err != nil {
			log.Fatalf("Failed to create release branch in %s: %v", service, err)
		}
	}

	// Phase 6: Commit changes for all
	fmt.Println("\nPhase 6: Committing changes...")
	commitMsg := fmt.Sprintf("Up to version %s.0", version)
	for _, service := range services {
		fmt.Printf("  Committing service: %s\n", service)
		if err := gitAddAll(serviceDirs[service]); err != nil {
			log.Fatalf("Failed to add files in %s: %v", service, err)
		}
		if err := gitCommit(serviceDirs[service], commitMsg); err != nil {
			log.Fatalf("Failed to commit in %s: %v", service, err)
		}
	}

	// Wait for user confirmation
	fmt.Println("\nAll changes have been prepared. Please review the changes.")
	fmt.Println("Press Enter to continue and push changes...")
	reader := bufio.NewReader(os.Stdin)
	reader.ReadString('\n')

	// Phase 7: Push changes for all
	fmt.Println("\nPhase 7: Pushing changes...")
	for _, service := range services {
		fmt.Printf("  Pushing service: %s\n", service)
		if err := gitPush(serviceDirs[service]); err != nil {
			log.Fatalf("Failed to push in %s: %v", service, err)
		}
	}

	fmt.Println("\nDeployment script completed successfully!")
}

func readConfig(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var services []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			services = append(services, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return services, nil
}

func checkGitClean(dir string) error {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	if len(output) > 0 {
		return fmt.Errorf("working directory has uncommitted changes")
	}

	return nil
}

func gitCheckout(dir string, args ...string) error {
	cmdArgs := append([]string{"checkout"}, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func gitPull(dir string) error {
	cmd := exec.Command("git", "pull")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func gitAddAll(dir string) error {
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func gitCommit(dir string, message string) error {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func gitPush(dir string) error {
	cmd := exec.Command("git", "push", "-u", "origin", "HEAD")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func updatePomFiles(dir string, version string) error {
	// Find all pom.xml files
	var pomFiles []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Name() == "pom.xml" {
			pomFiles = append(pomFiles, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Update each pom.xml
	for _, pomFile := range pomFiles {
		if err := updatePomFile(pomFile, version); err != nil {
			return fmt.Errorf("failed to update %s: %v", pomFile, err)
		}
	}

	return nil
}

func updatePomFile(filename string, version string) error {
	// Read file
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}

	// Parse XML
	var pom POM
	if err := xml.Unmarshal(data, &pom); err != nil {
		return err
	}

	// Update version
	pom.Version = version + ".0"

	// Update properties containing "proezd"
	if pom.Properties != nil {
		updatedProps := updateProezdProperties(string(pom.Properties.Inner), version)
		pom.Properties.Inner = []byte(updatedProps)
	}

	// Marshal back to XML
	output, err := xml.MarshalIndent(pom, "", "    ")
	if err != nil {
		return err
	}

	// Add XML declaration
	finalOutput := []byte(xml.Header + string(output))

	// Write file
	return ioutil.WriteFile(filename, finalOutput, 0644)
}

func updateProezdProperties(props string, version string) string {
	lines := strings.Split(props, "\n")
	var result []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "proezd") && strings.Contains(trimmed, "<") && strings.Contains(trimmed, ">") {
			// Extract property name
			start := strings.Index(trimmed, "<")
			end := strings.Index(trimmed, ">")
			if start != -1 && end != -1 && start < end {
				propName := trimmed[start+1 : end]
				if strings.Contains(propName, "proezd") {
					// Replace version
					closeTag := "</" + propName + ">"
					newLine := fmt.Sprintf("        <%s>%s.0%s", propName, version, closeTag)
					result = append(result, newLine)
					continue
				}
			}
		}
		result = append(result, line)
	}

	return strings.Join(result, "\n")
}
