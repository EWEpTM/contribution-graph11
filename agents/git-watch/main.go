package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tomer-Barak/contribution-graph/agents/lib"
)

// Config holds the agent configuration
type Config struct {
	ServerURL string
	Since     string
	DryRun    bool
}

func main() {
	// Parse configuration
	config := Config{
		ServerURL: lib.GetEnv("SERVER_URL", "http://localhost:8080"),
		Since:     lib.GetEnv("SINCE", "24 hours ago"),
		DryRun:    os.Getenv("DRY_RUN") == "true",
	}

	// Determine root directory to scan
	rootDir := "./"
	if len(os.Args) > 1 {
		rootDir = os.Args[1]
	}

	fmt.Printf("🔍 Scanning for git repos in: %s\n", rootDir)
	fmt.Printf("   Looking for commits since: %s\n", config.Since)

	// Find all repositories
	repos, err := findGitRepos(rootDir)
	if err != nil {
		logError(err)
		os.Exit(1)
	}

	fmt.Printf("📁 Found %d git repositories\n", len(repos))

	var allContributions []lib.Contribution

	// Extract commits from each repo
	for _, repoPath := range repos {
		commits, err := getGitCommits(repoPath, config.Since)
		if err != nil {
			fmt.Printf("⚠️  Could not read repo %s: %v\n", repoPath, err)
			continue
		}
		if len(commits) > 0 {
			fmt.Printf("   📝 %s: %d commits\n", filepath.Base(repoPath), len(commits))
		}
		allContributions = append(allContributions, commits...)
	}

	fmt.Printf("\n✅ Total contributions found: %d\n", len(allContributions))

	if len(allContributions) == 0 {
		fmt.Println("No new contributions to sync.")
		return
	}

	// Output or send data
	if config.DryRun {
		jsonData, _ := json.MarshalIndent(allContributions, "", "  ")
		fmt.Println("\n📄 Dry run output (JSON):")
		fmt.Println(string(jsonData))
	} else {
		err := lib.Push(config.ServerURL, allContributions)
		if err != nil {
			fmt.Printf("❌ Upload failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("🎉 Successfully uploaded %d contributions to %s\n", len(allContributions), config.ServerURL)
	}
}

// findGitRepos walks the directory tree looking for .git folders
func findGitRepos(root string) ([]string, error) {
	var repos []string

	// Resolve absolute path
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	err = filepath.Walk(absRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip permission denied errors
		}

		// Skip common non-project directories
		if info.IsDir() {
			name := info.Name()
			if name == "node_modules" || name == "vendor" || name == ".cache" || name == "__pycache__" {
				return filepath.SkipDir
			}
		}

		if info.IsDir() && info.Name() == ".git" {
			// Found a git dir, add the parent to our list
			parentDir := filepath.Dir(path)
			repos = append(repos, parentDir)
			return filepath.SkipDir // Don't look inside .git
		}
		return nil
	})
	return repos, err
}

// getGitCommits runs the git log command in the specific folder
func getGitCommits(repoPath, since string) ([]lib.Contribution, error) {
	// Format: Hash|ISO-Date|Email|Subject
	// %H = Hash, %aI = Author Date (ISO 8601), %ae = Email, %s = Subject
	cmd := exec.Command("git", "log", "--since="+since, "--pretty=format:%H|%aI|%ae|%s")
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		// 真实错误透传给调用方，不再静默当成"无提交"
		return nil, fmt.Errorf("git log failed in %s: %v", repoPath, err)
	}

	var contributions []lib.Contribution
	lines := strings.Split(string(out), "\n")
	repoName := filepath.Base(repoPath)

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}

		// Parse timestamp
		t, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue
		}

		c := lib.Contribution{
			Source:    "git",
			Context:   repoName,
			Timestamp: t,
			MetaData: map[string]interface{}{
				"hash":    parts[0],
				"author":  parts[2],
				"message": lib.Truncate(parts[3], 100),
			},
		}
		contributions = append(contributions, c)
	}

	return contributions, nil
}

func logError(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}
