package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const changelogFile = "changelog.md"

type ChangelogEntry struct {
	Version     string
	Summary     string
	Description string
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	switch command {
	case "version":
		if err := versionCommand(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "release":
		if err := releaseCommand(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: release-tool <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  version    Print the latest version from changelog.md")
	fmt.Println("  release    Create a release tag and push to origin")
}

func versionCommand() error {
	entry, err := parseLatestChangelogEntry()
	if err != nil {
		return err
	}
	fmt.Println(entry.Version)
	return nil
}

func releaseCommand() error {
	entry, err := parseLatestChangelogEntry()
	if err != nil {
		return err
	}

	fmt.Println("Release Info:")
	fmt.Printf("  Version: v%s\n", entry.Version)
	fmt.Printf("  Title: %s\n", entry.Summary)

	if err := ensureGitRepo(); err != nil {
		return err
	}
	if err := ensureOriginRemote(); err != nil {
		return err
	}
	if err := fetchTags(); err != nil {
		return err
	}
	if err := ensureTagAbsent(entry.Version); err != nil {
		return err
	}

	if err := gitAddAll(); err != nil {
		return err
	}
	committed, err := gitCommitIfNeeded(entry.Summary, entry.Description)
	if err != nil {
		return err
	}
	if committed {
		fmt.Println("Committed staged changes.")
	}

	tag, err := gitTag(entry.Version, entry.Summary, entry.Description)
	if err != nil {
		return err
	}
	fmt.Printf("Created tag v%s.\n", entry.Version)

	if err := gitPush(tag); err != nil {
		return err
	}

	fmt.Printf("\nRelease complete: %s (v%s)\n", entry.Summary, entry.Version)
	return nil
}

func parseLatestChangelogEntry() (*ChangelogEntry, error) {
	file, err := os.Open(changelogFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", changelogFile, err)
	}
	defer file.Close()

	headerRegex := regexp.MustCompile(`^#\s*([0-9]+(?:\.[0-9]+){1,2}(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)\s*-\s*(.+)$`)
	
	scanner := bufio.NewScanner(file)
	var entry ChangelogEntry
	collecting := false
	var bulletLines []string

	for scanner.Scan() {
		line := scanner.Text()
		
		if strings.HasPrefix(line, "#") {
			matches := headerRegex.FindStringSubmatch(line)
			if matches == nil {
				continue
			}
			if !collecting {
				entry.Version = strings.TrimSpace(matches[1])
				entry.Summary = strings.TrimSpace(matches[2])
				collecting = true
				continue
			}
			break
		}
		
		if collecting {
			trimmed := strings.TrimSpace(line)
			if after, found := strings.CutPrefix(trimmed, "-"); found {
				bullet := strings.TrimSpace(after)
				bulletLines = append(bulletLines, bullet)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading %s: %w", changelogFile, err)
	}

	if !collecting || entry.Summary == "" {
		return nil, fmt.Errorf("unable to parse latest changelog entry in %s", changelogFile)
	}

	if len(bulletLines) > 0 {
		for i, bullet := range bulletLines {
			bulletLines[i] = "- " + bullet
		}
		entry.Description = strings.Join(bulletLines, "\n")
	}

	return &entry, nil
}

func ensureGitRepo() error {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		return fmt.Errorf("not a git repository. Initialize and set up remotes first")
	}
	return nil
}

func ensureOriginRemote() error {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("no 'origin' remote set. Add one (git remote add origin ...)")
	}
	return nil
}

func fetchTags() error {
	cmd := exec.Command("git", "fetch", "--tags")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to fetch tags: %w", err)
	}
	return nil
}

func ensureTagAbsent(version string) error {
	tag := "v" + version
	cmd := exec.Command("git", "rev-parse", tag)
	if err := cmd.Run(); err == nil {
		return fmt.Errorf("tag %s already exists. Update changelog.md before releasing", tag)
	}
	return nil
}

func gitAddAll() error {
	cmd := exec.Command("git", "add", "-A")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}
	return nil
}

func gitCommitIfNeeded(summary, description string) (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check staged changes: %w", err)
	}

	if strings.TrimSpace(string(output)) == "" {
		fmt.Println("No staged changes to commit.")
		return false, nil
	}

	args := []string{"commit", "-m", summary}
	if description != "" {
		args = append(args, "-m", description)
	}
	
	cmd = exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("failed to commit: %w", err)
	}
	return true, nil
}

func gitTag(version, summary, description string) (string, error) {
	tag := "v" + version
	message := summary
	if description != "" {
		message = summary + "\n\n" + description
	}
	
	cmd := exec.Command("git", "tag", "-a", tag, "-m", message)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create tag: %w", err)
	}
	return tag, nil
}

func gitPush(tag string) error {
	cmd := exec.Command("git", "push", "origin", "HEAD")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to push commits: %w", err)
	}

	cmd = exec.Command("git", "push", "origin", tag)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to push tag: %w", err)
	}
	return nil
}


