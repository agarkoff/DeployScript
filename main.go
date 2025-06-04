package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
)

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
			fmt.Printf("\nWarning: Git working copy is not clean in %s\n", service)

			// Show git status
			if err := showGitStatus(serviceDirs[service]); err != nil {
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
			if err := cleanGitWorkingDirectory(serviceDirs[service]); err != nil {
				log.Fatalf("Failed to clean working directory in %s: %v", service, err)
			}
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

		// Delete branch if it already exists (locally and remotely)
		if err := deleteBranchIfExists(serviceDirs[service], branchName); err != nil {
			log.Fatalf("Failed to delete existing branch in %s: %v", service, err)
		}

		// Create new branch
		if err := gitCheckout(serviceDirs[service], "-b", branchName); err != nil {
			log.Fatalf("Failed to create release branch in %s: %v", service, err)
		}
	}

	// Show all diffs before committing
	fmt.Println("\nShowing all changes before commit:")
	fmt.Println(strings.Repeat("=", 80))
	for _, service := range services {
		fmt.Printf("\n--- Changes in service: %s ---\n", service)
		if err := showGitDiff(serviceDirs[service]); err != nil {
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
		if err := gitAddAll(serviceDirs[service]); err != nil {
			log.Fatalf("Failed to add files in %s: %v", service, err)
		}
		if err := gitCommit(serviceDirs[service], commitMsg); err != nil {
			log.Fatalf("Failed to commit in %s: %v", service, err)
		}
	}

	// Phase 7: Create tags for all
	fmt.Println("\nPhase 7: Creating tags...")
	tagName := fmt.Sprintf("release/%s.0", version)
	for _, service := range services {
		fmt.Printf("  Creating tag for service: %s\n", service)

		// Delete tag if it already exists (locally and remotely)
		if err := deleteTagIfExists(serviceDirs[service], tagName); err != nil {
			log.Fatalf("Failed to delete existing tag in %s: %v", service, err)
		}

		// Create new tag
		if err := gitTag(serviceDirs[service], tagName); err != nil {
			log.Fatalf("Failed to create tag in %s: %v", service, err)
		}
	}

	// Wait for user confirmation
	fmt.Println("\nAll changes have been prepared. Please review the changes.")
	fmt.Println("Press Enter to continue and push changes...")
	reader := bufio.NewReader(os.Stdin)
	reader.ReadString('\n')

	// Phase 8: Push changes and tags for all
	fmt.Println("\nPhase 8: Pushing changes and tags...")
	for _, service := range services {
		fmt.Printf("  Pushing service: %s\n", service)
		if err := gitPushWithTags(serviceDirs[service]); err != nil {
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
	// First, update the index to refresh cached file stats
	cmd := exec.Command("git", "update-index", "--refresh")
	cmd.Dir = dir
	cmd.Run() // Ignore errors, as it returns non-zero if there are changes

	// Now check if there are any changes to tracked files
	cmd = exec.Command("git", "diff-index", "--quiet", "HEAD", "--")
	cmd.Dir = dir
	err := cmd.Run()

	if err != nil {
		// Exit code 1 means there are changes, other errors are real problems
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return fmt.Errorf("working directory has uncommitted changes")
		}
		return err
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

func gitTag(dir string, tagName string) error {
	cmd := exec.Command("git", "tag", tagName)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func gitPushWithTags(dir string) error {
	// First, push the branch and tags with force to overwrite remote
	cmd := exec.Command("git", "push", "-u", "origin", "HEAD", "--tags", "--force-with-lease")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func deleteBranchIfExists(dir string, branchName string) error {
	// Try to delete local branch (ignore error if it doesn't exist)
	cmd := exec.Command("git", "branch", "-D", branchName)
	cmd.Dir = dir
	cmd.Run() // Ignore error, branch might not exist

	// Try to delete remote branch (ignore error if it doesn't exist)
	cmd = exec.Command("git", "push", "origin", "--delete", branchName)
	cmd.Dir = dir
	cmd.Run() // Ignore error, remote branch might not exist

	return nil
}

func deleteTagIfExists(dir string, tagName string) error {
	// Try to delete local tag (ignore error if it doesn't exist)
	cmd := exec.Command("git", "tag", "-d", tagName)
	cmd.Dir = dir
	cmd.Run() // Ignore error, tag might not exist

	// Try to delete remote tag (ignore error if it doesn't exist)
	cmd = exec.Command("git", "push", "origin", ":refs/tags/"+tagName)
	cmd.Dir = dir
	cmd.Run() // Ignore error, remote tag might not exist

	return nil
}

func showGitStatus(dir string) error {
	cmd := exec.Command("git", "status")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func showGitDiff(dir string) error {
	cmd := exec.Command("git", "diff")
	cmd.Dir = dir

	// Capture output to process it
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		// If there's no diff, git diff returns 0, so this is a real error
		return err
	}

	// Process the output line by line
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Text()
		coloredLine := colorizeGitDiffLine(line)
		fmt.Println(coloredLine)
	}

	return scanner.Err()
}

func colorizeGitDiffLine(line string) string {
	if len(line) == 0 {
		return line
	}

	switch line[0] {
	case '-':
		// Lines starting with --- are file headers, not deletions
		if strings.HasPrefix(line, "---") {
			return colorCyan + line + colorReset
		}
		// Deleted lines
		return colorRed + line + colorReset
	case '+':
		// Lines starting with +++ are file headers, not additions
		if strings.HasPrefix(line, "+++") {
			return colorCyan + line + colorReset
		}
		// Added lines
		return colorGreen + line + colorReset
	case '@':
		// Hunk headers
		return colorCyan + line + colorReset
	case 'd':
		// diff headers
		if strings.HasPrefix(line, "diff ") {
			return colorYellow + line + colorReset
		}
		return line
	case 'i':
		// index headers
		if strings.HasPrefix(line, "index ") {
			return colorYellow + line + colorReset
		}
		return line
	default:
		return line
	}
}

func cleanGitWorkingDirectory(dir string) error {
	// Reset all tracked files to HEAD
	cmd := exec.Command("git", "reset", "--hard", "HEAD")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to reset: %v: %s", err, output)
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
		// Check if this is a root pom (in the service's top directory)
		isRootPom := filepath.Dir(pomFile) == dir

		if err := updatePomFile(pomFile, version, isRootPom); err != nil {
			return fmt.Errorf("failed to update %s: %v", pomFile, err)
		}
	}

	return nil
}

func updatePomFile(filename string, version string, isRootPom bool) error {
	// Read file
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}

	content := string(data)
	newVersion := version + ".0"

	// Parse line by line
	lines := strings.Split(content, "\n")

	// Flags for tracking context
	insideProject := false
	insideParent := false
	insideProperties := false

	// Counters for tracking what we've updated
	rootVersionUpdated := false
	parentVersionUpdated := false

	// Counter for tags after project
	tagsAfterProject := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track entering/exiting project
		if strings.Contains(line, "<project") {
			insideProject = true
			tagsAfterProject = 0
		}

		// Track entering/exiting parent
		if strings.Contains(line, "<parent>") {
			insideParent = true
		} else if strings.Contains(line, "</parent>") {
			insideParent = false
		}

		// Track entering/exiting properties
		if strings.Contains(line, "<properties>") {
			insideProperties = true
		} else if strings.Contains(line, "</properties>") {
			insideProperties = false
		}

		// Count tags after project (to determine if version is direct child)
		if insideProject && !insideParent && !insideProperties {
			if strings.Contains(trimmed, "<") && !strings.Contains(trimmed, "</") &&
				!strings.Contains(trimmed, "<version>") {
				tagsAfterProject++
			}
		}

		// Update version tags
		if strings.Contains(trimmed, "<version>") && strings.Contains(trimmed, "</version>") {

			// Extract current version
			start := strings.Index(trimmed, "<version>") + 9
			end := strings.Index(trimmed, "</version>")

			if start > 8 && end > start {
				currentVersion := trimmed[start:end]

				// CASE 1: Root POM - update version that's direct child of project
				if isRootPom && insideProject && !insideParent && !insideProperties &&
					!rootVersionUpdated && tagsAfterProject <= 4 {
					// Replace version
					newLine := strings.Replace(line, "<version>"+currentVersion+"</version>",
						"<version>"+newVersion+"</version>", 1)
					lines[i] = newLine
					rootVersionUpdated = true
				}

				// CASE 2a: Submodule POM - update version inside parent
				if !isRootPom && insideParent && !parentVersionUpdated {
					newLine := strings.Replace(line, "<version>"+currentVersion+"</version>",
						"<version>"+newVersion+"</version>", 1)
					lines[i] = newLine
					parentVersionUpdated = true
				}

				// CASE 2b: Submodule POM - update project version
				if !isRootPom && insideProject && !insideParent && !insideProperties &&
					!rootVersionUpdated && tagsAfterProject <= 4 {
					newLine := strings.Replace(line, "<version>"+currentVersion+"</version>",
						"<version>"+newVersion+"</version>", 1)
					lines[i] = newLine
					rootVersionUpdated = true
				}
			}
		}

		// CASE 3: Update proezd properties
		if insideProperties && strings.Contains(trimmed, "proezd") &&
			strings.Contains(trimmed, "<") && strings.Contains(trimmed, ">") {
			// Find property tag with proezd in name
			startTag := strings.Index(trimmed, "<")
			endTag := strings.Index(trimmed, ">")

			if startTag >= 0 && endTag > startTag {
				tagContent := trimmed[startTag+1 : endTag]

				// Check if this is a proezd property (not a closing tag)
				if strings.Contains(tagContent, "proezd") && !strings.HasPrefix(tagContent, "/") {
					// Find the value
					valueStart := endTag + 1
					valueEnd := strings.Index(trimmed[valueStart:], "<")

					if valueEnd > 0 {
						// Replace the value
						oldValue := trimmed[valueStart : valueStart+valueEnd]
						newLine := strings.Replace(line, ">"+oldValue+"<", ">"+newVersion+"<", 1)
						lines[i] = newLine
					}
				}
			}
		}
	}

	// Join lines back
	content = strings.Join(lines, "\n")

	// Write file back
	return ioutil.WriteFile(filename, []byte(content), 0644)
}
