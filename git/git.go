package git

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
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
