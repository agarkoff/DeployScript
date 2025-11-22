package git

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// ANSI color codes
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorCyan   = "\033[36m"
	ColorYellow = "\033[33m"
)

// CheckClean checks if git working directory is clean
func CheckClean(dir string) error {
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

// ShowStatus shows git status
func ShowStatus(dir string) error {
	cmd := exec.Command("git", "status")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CleanWorkingDirectory resets all tracked files to HEAD
func CleanWorkingDirectory(dir string) error {
	cmd := exec.Command("git", "reset", "--hard", "HEAD")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to reset: %v: %s", err, output)
	}
	return nil
}

// Checkout performs git checkout
func Checkout(dir string, args ...string) error {
	cmdArgs := append([]string{"checkout"}, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

// Pull performs git pull
func Pull(dir string) error {
	cmd := exec.Command("git", "pull")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

// AddAll stages all changes
func AddAll(dir string) error {
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

// Commit creates a commit with the given message
func Commit(dir string, message string) error {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

// Tag creates a tag
func Tag(dir string, tagName string) error {
	cmd := exec.Command("git", "tag", tagName)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

// PushWithTags pushes branch and tags
func PushWithTags(dir string) error {
	cmd := exec.Command("git", "push", "-u", "origin", "HEAD", "--tags", "--force-with-lease")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

// DeleteBranchIfExists deletes a branch locally and remotely if it exists
// It tries both / and - separators to handle old and new branch naming conventions
func DeleteBranchIfExists(dir string, branchName string) error {
	// Generate both possible branch names
	dashName := strings.ReplaceAll(branchName, "/", "-")
	slashName := strings.ReplaceAll(branchName, "-", "/")

	branchesToDelete := []string{dashName}
	if dashName != slashName {
		branchesToDelete = append(branchesToDelete, slashName)
	}

	// Try to delete local branches (ignore error if they don't exist)
	for _, branch := range branchesToDelete {
		cmd := exec.Command("git", "branch", "-D", branch)
		cmd.Dir = dir
		cmd.Run() // Ignore error, branch might not exist
	}

	// Try to delete remote branches (ignore error if they don't exist)
	for _, branch := range branchesToDelete {
		cmd := exec.Command("git", "push", "origin", "--delete", branch)
		cmd.Dir = dir
		cmd.Run() // Ignore error, remote branch might not exist
	}

	return nil
}

// DeleteTagIfExists deletes a tag locally and remotely if it exists
// It tries both / and - separators to handle old and new tag naming conventions
func DeleteTagIfExists(dir string, tagName string) error {
	// Generate both possible tag names
	dashName := strings.ReplaceAll(tagName, "/", "-")
	slashName := strings.ReplaceAll(tagName, "-", "/")

	tagsToDelete := []string{dashName}
	if dashName != slashName {
		tagsToDelete = append(tagsToDelete, slashName)
	}

	// Try to delete local tags (ignore error if they don't exist)
	for _, tag := range tagsToDelete {
		cmd := exec.Command("git", "tag", "-d", tag)
		cmd.Dir = dir
		cmd.Run() // Ignore error, tag might not exist
	}

	// Try to delete remote tags (ignore error if they don't exist)
	for _, tag := range tagsToDelete {
		cmd := exec.Command("git", "push", "origin", ":refs/tags/"+tag)
		cmd.Dir = dir
		cmd.Run() // Ignore error, remote tag might not exist
	}

	return nil
}

// ShowDiff shows git diff with color
func ShowDiff(dir string) error {
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
		coloredLine := colorizeDiffLine(line)
		fmt.Println(coloredLine)
	}

	return scanner.Err()
}

// colorizeDiffLine adds color to git diff output
func colorizeDiffLine(line string) string {
	if len(line) == 0 {
		return line
	}

	switch line[0] {
	case '-':
		// Lines starting with --- are file headers, not deletions
		if strings.HasPrefix(line, "---") {
			return ColorCyan + line + ColorReset
		}
		// Deleted lines
		return ColorRed + line + ColorReset
	case '+':
		// Lines starting with +++ are file headers, not additions
		if strings.HasPrefix(line, "+++") {
			return ColorCyan + line + ColorReset
		}
		// Added lines
		return ColorGreen + line + ColorReset
	case '@':
		// Hunk headers
		return ColorCyan + line + ColorReset
	case 'd':
		// diff headers
		if strings.HasPrefix(line, "diff ") {
			return ColorYellow + line + ColorReset
		}
		return line
	case 'i':
		// index headers
		if strings.HasPrefix(line, "index ") {
			return ColorYellow + line + ColorReset
		}
		return line
	default:
		return line
	}
}

// findRefWithBothSeparators tries to find a branch or tag with either / or - separator
// It returns the found ref name and whether it was found
func findRefWithBothSeparators(dir string, refType string, pattern string) (string, bool) {
	// Determine which separators to try based on the pattern
	var namesToTry []string

	if strings.Contains(pattern, "/") {
		// Pattern has /, try - version first (new format), then original
		dashName := strings.ReplaceAll(pattern, "/", "-")
		namesToTry = []string{dashName, pattern}
	} else if strings.Contains(pattern, "-") {
		// Pattern has -, try it first, then / version (old format)
		slashName := strings.ReplaceAll(pattern, "-", "/")
		namesToTry = []string{pattern, slashName}
	} else {
		// No separator in pattern, just try as-is
		namesToTry = []string{pattern}
	}

	for _, name := range namesToTry {
		var checkCmd *exec.Cmd
		if refType == "branch" {
			checkCmd = exec.Command("git", "rev-parse", "--verify", fmt.Sprintf("origin/%s", name))
		} else {
			checkCmd = exec.Command("git", "rev-parse", "--verify", name)
		}
		checkCmd.Dir = dir
		if err := checkCmd.Run(); err == nil {
			return name, true
		}
	}

	return "", false
}

// GetCurrentBranch returns the current branch name
func GetCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetCommitForTag returns the commit SHA for a given tag
func GetCommitForTag(dir string, tag string) (string, error) {
	cmd := exec.Command("git", "rev-list", "-n", "1", tag)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get commit for tag %s: %v: %s", tag, err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetPreviousReleaseBranch finds the previous release branch
func GetPreviousReleaseBranch(dir string, currentVersion int) (string, error) {
	// Try to find previous version branch
	previousVersion := currentVersion - 1
	if previousVersion < 1 {
		return "", fmt.Errorf("no previous version possible (current version is %d)", currentVersion)
	}

	// Try to find branch with both separators
	pattern := fmt.Sprintf("release/%d", previousVersion)
	foundBranch, found := findRefWithBothSeparators(dir, "branch", pattern)
	if !found {
		return "", fmt.Errorf("previous release branch release/%d or release-%d not found", previousVersion, previousVersion)
	}

	fmt.Printf("Found previous release branch: %s\n", foundBranch)
	return foundBranch, nil
}

// GetBranchStartCommit finds the commit where a branch was created from its parent
func GetBranchStartCommit(dir string, branchName string) (string, error) {
	// Find the merge-base between the branch and develop (assuming branches are created from develop)
	cmd := exec.Command("git", "merge-base", fmt.Sprintf("origin/%s", branchName), "origin/develop")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to find branch start commit: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetLastTagInBranch finds the last tag in a specific branch
func GetLastTagInBranch(dir string, branchName string) (string, error) {
	// Get all tags reachable from the branch - try both separators
	var allTags []string

	// Try with release/* pattern (old format with /)
	cmd := exec.Command("git", "tag", "--merged", fmt.Sprintf("origin/%s", branchName), "release/*")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err == nil && len(output) > 0 {
		tags := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, tag := range tags {
			if tag != "" {
				allTags = append(allTags, tag)
			}
		}
	}

	// Try with release-* pattern (new format with -)
	cmd = exec.Command("git", "tag", "--merged", fmt.Sprintf("origin/%s", branchName), "release-*")
	cmd.Dir = dir
	output, err = cmd.CombinedOutput()
	if err == nil && len(output) > 0 {
		tags := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, tag := range tags {
			if tag != "" && !contains(allTags, tag) {
				allTags = append(allTags, tag)
			}
		}
	}

	if len(allTags) == 0 {
		return "", fmt.Errorf("no tags found in branch %s", branchName)
	}

	// Sort tags to find the latest one
	sort.Strings(allTags)
	lastTag := allTags[len(allTags)-1]

	fmt.Printf("Found last tag in branch %s: %s\n", branchName, lastTag)
	return lastTag, nil
}

// contains checks if a string slice contains a specific string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// CommitInfo represents information about a commit
type CommitInfo struct {
	SHA     string
	Message string
	TaskID  string
}

// GetCommitsBetween returns commits between two references
func GetCommitsBetween(dir string, fromRef string, toRef string) ([]CommitInfo, error) {
	// Get commit logs between two references
	cmd := exec.Command("git", "log", "--pretty=format:%H|%s", fmt.Sprintf("%s..%s", fromRef, toRef))
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get commits: %v: %s", err, output)
	}

	if len(output) == 0 {
		return []CommitInfo{}, nil
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	commits := make([]CommitInfo, 0, len(lines))

	// Regex to match task IDs (2-10 letters followed by - and 5-6 digits)
	taskRegex := regexp.MustCompile(`([A-Za-z]{2,10})-(\d{5,6})`)

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}

		commit := CommitInfo{
			SHA:     parts[0],
			Message: parts[1],
		}

		// Find all task IDs in the message
		matches := taskRegex.FindAllString(commit.Message, -1)
		if len(matches) > 0 {
			// Для каждого найденного TaskID создается новый объект CommitInfo
			for _, taskID := range matches {
				newCommit := CommitInfo{
					SHA:     commit.SHA,
					Message: commit.Message,
					TaskID:  taskID,
				}
				commits = append(commits, newCommit)
			}
		}

		commits = append(commits, commit)
	}

	return commits, nil
}

// CreateReleaseNotes creates a release notes file with all tasks included in the release
func CreateReleaseNotes(dirs map[string]string, version int, taskURLPrefix string) error {
	filename := fmt.Sprintf("release-notes-%d.txt", version)

	// Find previous release branch from the first service
	var firstDir string
	for _, dir := range dirs {
		firstDir = dir
		break
	}

	prevBranch, err := GetPreviousReleaseBranch(firstDir, version)
	if err != nil {
		fmt.Printf("Warning: Could not find previous release branch: %v\n", err)
		fmt.Printf("Creating empty release notes file\n")

		// Create empty file
		content := fmt.Sprintf("Release Notes for Version %d\n", version)
		content += "=" + strings.Repeat("=", len(content)-1) + "\n\n"
		content += "No previous release branch found to compare against.\n"

		return os.WriteFile(filename, []byte(content), 0644)
	}

	fmt.Printf("\n=== Release Notes Generation ===\n")
	fmt.Printf("Current version: %d\n", version)
	fmt.Printf("Previous release branch: %s\n", prevBranch)

	// Collect all tasks from all services
	allTasksBetweenReleases := make(map[string]bool)
	tasksInPreviousRelease := make(map[string]bool)
	serviceStats := make(map[string]struct {
		TotalCommits int
		TasksFound   int
		LastTag      string
	})

	fmt.Printf("\n=== Processing Services ===\n")
	for service, dir := range dirs {
		fmt.Printf("\n--- Service: %s ---\n", service)

		// Get branch start commit for previous release FOR THIS SERVICE
		prevBranchStart, err := GetBranchStartCommit(dir, prevBranch)
		if err != nil {
			fmt.Printf("Warning: Could not find start of previous release branch for %s: %v\n", service, err)
			continue
		}
		fmt.Printf("Previous release branch start commit: %s\n", prevBranchStart)

		// Get last tag in previous release branch FOR THIS SERVICE
		lastTagInPrevBranch, err := GetLastTagInBranch(dir, prevBranch)
		if err != nil {
			fmt.Printf("Warning: Could not find last tag in previous branch for %s, using branch tip instead: %v\n", service, err)
			lastTagInPrevBranch = fmt.Sprintf("origin/%s", prevBranch)
		}

		// Get commit for the last tag
		var lastCommitInPrevBranch string
		if strings.HasPrefix(lastTagInPrevBranch, "origin/") {
			// It's a branch reference, get its commit
			cmd := exec.Command("git", "rev-parse", lastTagInPrevBranch)
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if err != nil {
				fmt.Printf("Warning: Failed to get commit for branch %s in service %s: %v\n", lastTagInPrevBranch, service, err)
				continue
			}
			lastCommitInPrevBranch = strings.TrimSpace(string(output))
		} else {
			// It's a tag, get its commit
			lastCommitInPrevBranch, err = GetCommitForTag(dir, lastTagInPrevBranch)
			if err != nil {
				fmt.Printf("Warning: Failed to get commit for tag %s in service %s: %v\n", lastTagInPrevBranch, service, err)
				continue
			}
		}
		fmt.Printf("Last commit in previous release: %s (from %s)\n", lastCommitInPrevBranch, lastTagInPrevBranch)

		// Get tasks between releases (from last commit of previous release to current HEAD)
		fmt.Printf("Getting commits between %s and HEAD...\n", lastCommitInPrevBranch)
		commitsBetweenReleases, err := GetCommitsBetween(dir, lastCommitInPrevBranch, "HEAD")
		if err != nil {
			fmt.Printf("Warning: Could not get commits between releases for %s: %v\n", service, err)
			continue
		}

		tasksFoundBetween := 0
		serviceTasksBetween := []string{}
		for _, commit := range commitsBetweenReleases {
			if commit.TaskID != "" {
				allTasksBetweenReleases[commit.TaskID] = true
				serviceTasksBetween = append(serviceTasksBetween, commit.TaskID)
				tasksFoundBetween++
			}
		}
		fmt.Printf("Found %d commits, %d with task IDs\n", len(commitsBetweenReleases), tasksFoundBetween)
		if len(serviceTasksBetween) > 0 {
			fmt.Println("Tasks found between releases:")
			for _, taskID := range serviceTasksBetween {
				fmt.Printf("  %s\n", taskID)
			}
		}

		// Get tasks within previous release (from branch start to last tag/commit)
		fmt.Printf("Getting commits in previous release (between %s and %s)...\n", prevBranchStart, lastCommitInPrevBranch)
		commitsInPrevRelease, err := GetCommitsBetween(dir, prevBranchStart, lastCommitInPrevBranch)
		if err != nil {
			fmt.Printf("Warning: Could not get commits in previous release for %s: %v\n", service, err)
			continue
		}

		tasksFoundInPrev := 0
		serviceTasksInPrev := []string{}
		for _, commit := range commitsInPrevRelease {
			if commit.TaskID != "" {
				tasksInPreviousRelease[commit.TaskID] = true
				serviceTasksInPrev = append(serviceTasksInPrev, commit.TaskID)
				tasksFoundInPrev++
			}
		}
		fmt.Printf("Found %d commits in previous release, %d with task IDs\n", len(commitsInPrevRelease), tasksFoundInPrev)
		if len(serviceTasksInPrev) > 0 {
			fmt.Println("Tasks found in previous release:")
			for _, taskID := range serviceTasksInPrev {
				fmt.Printf("  %s\n", taskID)
			}
		}

		serviceStats[service] = struct {
			TotalCommits int
			TasksFound   int
			LastTag      string
		}{
			TotalCommits: len(commitsBetweenReleases),
			TasksFound:   tasksFoundBetween,
			LastTag:      lastTagInPrevBranch,
		}
	}

	// Calculate the difference: tasks between releases minus tasks in previous release
	newTasks := make(map[string]bool)
	for taskID := range allTasksBetweenReleases {
		if !tasksInPreviousRelease[taskID] {
			newTasks[taskID] = true
		}
	}

	fmt.Printf("\n=== Summary ===\n")
	fmt.Printf("Total tasks between releases: %d\n", len(allTasksBetweenReleases))
	if len(allTasksBetweenReleases) > 0 {
		fmt.Println("All tasks between releases:")
		allTasksList := make([]string, 0, len(allTasksBetweenReleases))
		for taskID := range allTasksBetweenReleases {
			allTasksList = append(allTasksList, taskID)
		}
		sort.Strings(allTasksList)
		for _, taskID := range allTasksList {
			fmt.Printf("  %s\n", taskID)
		}
	}

	fmt.Printf("\nTasks in previous release: %d\n", len(tasksInPreviousRelease))
	if len(tasksInPreviousRelease) > 0 {
		fmt.Println("Tasks that were already in previous release:")
		prevTasksList := make([]string, 0, len(tasksInPreviousRelease))
		for taskID := range tasksInPreviousRelease {
			prevTasksList = append(prevTasksList, taskID)
		}
		sort.Strings(prevTasksList)
		for _, taskID := range prevTasksList {
			fmt.Printf("  %s\n", taskID)
		}
	}

	fmt.Printf("\nNew tasks in this release: %d\n", len(newTasks))
	if len(newTasks) > 0 {
		fmt.Println("New tasks (will be included in release notes):")
		for _, taskID := range newTasks {
			fmt.Printf("  %s\n", taskID)
		}
	}
	fmt.Println()

	// Sort task IDs for the final list
	taskIDs := make([]string, 0, len(newTasks))
	for taskID := range newTasks {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)

	// Create release notes content
	content := fmt.Sprintf("Release Notes for Version %d\n", version)
	content += "=" + strings.Repeat("=", len(content)-1) + "\n\n"
	content += fmt.Sprintf("Comparing with previous release branch: %s\n\n", prevBranch)

	// Add tasks section
	if len(taskIDs) > 0 {
		content += "Tasks included in this release:\n"
		content += strings.Repeat("-", 30) + "\n\n"

		for _, taskID := range taskIDs {
			if taskURLPrefix != "" {
				content += fmt.Sprintf("%s%s\n", taskURLPrefix, taskID)
			} else {
				content += fmt.Sprintf("%s\n", taskID)
			}
		}
		content += fmt.Sprintf("\nTotal new tasks: %d\n", len(taskIDs))
	} else {
		content += "No new tasks with IDs found in commit messages.\n"
	}

	// Add service statistics with last tags
	content += "\n\nService Statistics:\n"
	content += strings.Repeat("-", 50) + "\n"
	content += fmt.Sprintf("%-30s %-20s %s\n", "Service", "Last Tag", "Stats")
	content += strings.Repeat("-", 50) + "\n"

	// Sort services for consistent output
	sortedServices := make([]string, 0, len(serviceStats))
	for service := range serviceStats {
		sortedServices = append(sortedServices, service)
	}
	sort.Strings(sortedServices)

	for _, service := range sortedServices {
		stats := serviceStats[service]
		content += fmt.Sprintf("%-30s %-20s %d commits, %d tasks\n",
			service, stats.LastTag, stats.TotalCommits, stats.TasksFound)
	}

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write release notes: %v", err)
	}

	fmt.Printf("\n%sRelease notes created: %s%s\n", ColorGreen, filename, ColorReset)
	return nil
}
