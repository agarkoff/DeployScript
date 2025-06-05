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
func DeleteBranchIfExists(dir string, branchName string) error {
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

// DeleteTagIfExists deletes a tag locally and remotely if it exists
func DeleteTagIfExists(dir string, tagName string) error {
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