package maven

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CleanCache cleans the Maven cache for the specified path
func CleanCache(cachePath string) error {
	// Get Maven local repository path
	mavenRepo := GetLocalRepository()

	// Construct full path
	targetPath := filepath.Join(mavenRepo, cachePath)

	fmt.Printf("Cleaning Maven cache: %s\n", targetPath)

	// Check if directory exists
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		fmt.Println("Maven cache directory does not exist, skipping cleanup")
		return nil
	}

	// Remove the directory
	if err := os.RemoveAll(targetPath); err != nil {
		return fmt.Errorf("failed to remove Maven cache directory: %v", err)
	}

	fmt.Println("Maven cache cleaned successfully")
	return nil
}

// GetLocalRepository returns the Maven local repository path
func GetLocalRepository() string {
	// First, try to get from M2_REPO environment variable
	if m2Repo := os.Getenv("M2_REPO"); m2Repo != "" {
		return m2Repo
	}

	// Then, try to get from MAVEN_OPTS or standard location
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("Warning: Could not get user home directory: %v", err)
		homeDir = ""
	}

	// Default Maven repository location
	if homeDir != "" {
		return filepath.Join(homeDir, ".m2", "repository")
	}

	// Fallback for Windows
	if runtime.GOOS == "windows" {
		if userProfile := os.Getenv("USERPROFILE"); userProfile != "" {
			return filepath.Join(userProfile, ".m2", "repository")
		}
	}

	log.Fatal("Could not determine Maven local repository path")
	return ""
}

// BuildService builds a service using Maven
func BuildService(serviceDir string) error {
	// Create Maven command
	cmd := exec.Command("mvn", "clean", "install", "-DskipTests=true")
	cmd.Dir = serviceDir

	// Capture output
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Also print output in real-time
	cmd.Stdout = io.MultiWriter(&stdout, os.Stdout)
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)

	// Run the build
	err := cmd.Run()

	if err != nil {
		// Print error details
		fmt.Printf("\n\033[31mBuild failed!\033[0m\n")
		if stderr.Len() > 0 {
			fmt.Printf("Error output:\n%s\n", stderr.String())
		}
		return fmt.Errorf("mvn clean install failed: %v", err)
	}

	return nil
}

// UpdatePomFiles updates all pom.xml files in the directory with the new version
func UpdatePomFiles(dir string, version string, propertyPattern string) error {
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

		if err := UpdatePomFile(pomFile, version, isRootPom, propertyPattern); err != nil {
			return fmt.Errorf("failed to update %s: %v", pomFile, err)
		}
	}

	return nil
}

// UpdatePomFile updates a single pom.xml file with the new version
func UpdatePomFile(filename string, version string, isRootPom bool, propertyPattern string) error {
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

		// CASE 3: Update properties matching the pattern
		if insideProperties && strings.Contains(trimmed, propertyPattern) &&
			strings.Contains(trimmed, "<") && strings.Contains(trimmed, ">") {
			// Find property tag with pattern in name
			startTag := strings.Index(trimmed, "<")
			endTag := strings.Index(trimmed, ">")

			if startTag >= 0 && endTag > startTag {
				tagContent := trimmed[startTag+1 : endTag]

				// Check if this is a property matching pattern (not a closing tag)
				if strings.Contains(tagContent, propertyPattern) && !strings.HasPrefix(tagContent, "/") {
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

// BuildMeshService builds a mesh service using Maven with special sequence:
// 1. First builds graphql-mesh-resources submodule
// 2. Then builds the main project
func BuildMeshService(serviceDir string) error {
	// Step 1: Build graphql-mesh-resources first
	meshResourcesDir := filepath.Join(serviceDir, "graphql-mesh-resources")

	// Check if graphql-mesh-resources directory exists
	if _, err := os.Stat(meshResourcesDir); os.IsNotExist(err) {
		return fmt.Errorf("graphql-mesh-resources directory not found in %s", serviceDir)
	}

	fmt.Printf("  Building graphql-mesh-resources first...\n")

	// Create Maven command for mesh resources
	cmd := exec.Command("mvn", "clean", "install")
	cmd.Dir = meshResourcesDir

	// Capture and display output
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, os.Stdout)
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)

	// Run the build for mesh resources
	if err := cmd.Run(); err != nil {
		fmt.Printf("\n\033[31mBuild failed for graphql-mesh-resources!\033[0m\n")
		if stderr.Len() > 0 {
			fmt.Printf("Error output:\n%s\n", stderr.String())
		}
		return fmt.Errorf("mvn clean install failed in graphql-mesh-resources: %v", err)
	}

	fmt.Printf("  graphql-mesh-resources built successfully\n")

	// Step 2: Build the main project
	fmt.Printf("  Building main project...\n")

	// Create Maven command for main project
	cmd = exec.Command("mvn", "clean", "install")
	cmd.Dir = serviceDir

	// Reset buffers
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = io.MultiWriter(&stdout, os.Stdout)
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)

	// Run the main build
	if err := cmd.Run(); err != nil {
		fmt.Printf("\n\033[31mBuild failed for main project!\033[0m\n")
		if stderr.Len() > 0 {
			fmt.Printf("Error output:\n%s\n", stderr.String())
		}
		return fmt.Errorf("mvn clean install failed in main project: %v", err)
	}

	return nil
}
