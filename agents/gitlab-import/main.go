package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Configuration - Set these via environment variables
var (
	GitLabUsername = os.Getenv("GITLAB_USERNAME")
	GitLabToken    = os.Getenv("GITLAB_TOKEN")
	GitLabHost     = getEnv("GITLAB_HOST", "gitlab.com")
	LocalServerURL = getEnv("SERVER_URL", "http://localhost:8080/api/contributions")
)

// GitLab REST API response structures
type GitLabUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type GitLabEvent struct {
	ID        int       `json:"id"`
	UserID    int       `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	EventType string    `json:"action_name"`
	PushData  struct {
		CommitCount int `json:"commit_count"`
	} `json:"push_data"`
}

// Your App's Schema
type Contribution struct {
	Source    string   `json:"source"`
	Context   string   `json:"context"`
	Timestamp string   `json:"timestamp"`
	MetaData  MetaData `json:"metadata"`
}

type MetaData struct {
	ImportedFromGitLab bool   `json:"imported_from_gitlab"`
	OriginalCount      int    `json:"original_count,omitempty"`
	ImportDate         string `json:"import_date"`
	GitLabHost         string `json:"gitlab_host"`
}

func main() {
	// Validate configuration
	if GitLabUsername == "" {
		fmt.Println("❌ Error: GITLAB_USERNAME environment variable is required")
		fmt.Println("\nUsage:")
		fmt.Println("  export GITLAB_USERNAME=your-username")
		fmt.Println("  export GITLAB_TOKEN=your-personal-access-token")
		fmt.Println("  export GITLAB_HOST=git.example.com (optional, defaults to gitlab.com)")
		fmt.Println("  go run main.go")
		os.Exit(1)
	}

	if GitLabToken == "" {
		fmt.Println("❌ Error: GITLAB_TOKEN environment variable is required")
		fmt.Println("\nCreate a Personal Access Token at:")
		if GitLabHost == "gitlab.com" {
			fmt.Println("  https://gitlab.com/-/user_settings/personal_access_tokens")
		} else {
			fmt.Printf("  https://%s/-/user_settings/personal_access_tokens\n", GitLabHost)
		}
		fmt.Println("  (Select 'api' or 'read_api' scope)")
		os.Exit(1)
	}

	fmt.Printf("🚀 Fetching contribution data from GitLab (%s) for user: %s\n", GitLabHost, GitLabUsername)

	// Get user info first
	userURL := fmt.Sprintf("https://%s/api/v4/users?username=%s", GitLabHost, GitLabUsername)
	req, _ := http.NewRequest("GET", userURL, nil)
	req.Header.Set("PRIVATE-TOKEN", GitLabToken)
	req.Header.Set("User-Agent", "contribution-graph-importer")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Error fetching user info from GitLab: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Printf("❌ GitLab API returned status %d when fetching user\n", resp.StatusCode)
		os.Exit(1)
	}

	var users []GitLabUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		fmt.Printf("❌ Error parsing user JSON: %v\n", err)
		os.Exit(1)
	}

	if len(users) == 0 {
		fmt.Printf("❌ User '%s' not found\n", GitLabUsername)
		os.Exit(1)
	}

	userID := users[0].ID
	fmt.Printf("✅ Found user %s (ID: %d)\n", users[0].Name, userID)

	// Fetch user events (contributions)
	now := time.Now()
	// Start from the beginning of the current year
	startOfYear := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	after := startOfYear.Format("2006-01-02")

	eventsURL := fmt.Sprintf("https://%s/api/v4/users/%d/events?per_page=100&after=%s", GitLabHost, userID, after)

	// Group contributions by date
	contributionsByDate := make(map[string]int)
	page := 1

	fmt.Println("📥 Fetching events from GitLab...")

	for {
		pageURL := fmt.Sprintf("%s&page=%d", eventsURL, page)
		req, _ := http.NewRequest("GET", pageURL, nil)
		req.Header.Set("PRIVATE-TOKEN", GitLabToken)
		req.Header.Set("User-Agent", "contribution-graph-importer")

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("❌ Error fetching events: %v\n", err)
			os.Exit(1)
		}

		if resp.StatusCode != 200 {
			fmt.Printf("❌ GitLab API returned status %d\n", resp.StatusCode)
			resp.Body.Close()
			break
		}

		var events []GitLabEvent
		if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
			resp.Body.Close()
			break
		}
		resp.Body.Close()

		if len(events) == 0 {
			break
		}

		// Count contributions by date
		for _, event := range events {
			count := 0
			switch event.EventType {
			case "pushed", "pushed to", "pushed new":
				if event.PushData.CommitCount > 0 {
					count = event.PushData.CommitCount
				}
			case "created", "closed", "merged", "opened", "accepted", "approved", "commented on":
				count = 1
			}

			if count > 0 {
				date := event.CreatedAt.Format("2006-01-02")
				contributionsByDate[date] += count
			}
		}

		page++
		if page > 100 {
			break
		}
	}

	// Convert to Your App's Format
	var myContributions []Contribution
	importDate := time.Now().Format(time.RFC3339)
	totalContributions := 0

	for dateStr, count := range contributionsByDate {
		totalContributions += count

		// Parse date (YYYY-MM-DD)
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}

		// Create individual events for each contribution
		// This ensures proper heatmap intensity
		for i := 0; i < count; i++ {
			// Spread timestamps slightly to avoid exact duplicates
			timestamp := t.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)

			c := Contribution{
				Source:    "gitlab-import",
				Context:   "gitlab-history",
				Timestamp: timestamp,
				MetaData: MetaData{
					ImportedFromGitLab: true,
					OriginalCount:      count,
					ImportDate:         importDate,
					GitLabHost:         GitLabHost,
				},
			}
			myContributions = append(myContributions, c)
		}
	}

	fmt.Printf("📊 Total contributions found: %d\n", totalContributions)
	fmt.Printf("✅ Prepared %d contribution events for import\n", len(myContributions))

	// Check if dry run
	if os.Getenv("DRY_RUN") == "true" {
		if len(myContributions) > 0 {
			jsonData, _ := json.MarshalIndent(myContributions[:min(5, len(myContributions))], "", "  ")
			fmt.Println("\n📄 Dry run - Sample output (first 5 events):")
			fmt.Println(string(jsonData))
			if len(myContributions) > 5 {
				fmt.Printf("\n... and %d more events\n", len(myContributions)-5)
			}
		}
		return
	}

	// Push to Your Local Server
	if len(myContributions) > 0 {
		err := pushToServer(myContributions)
		if err != nil {
			fmt.Printf("❌ Error uploading: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("🎉 Successfully imported GitLab history to your local app!")
	} else {
		fmt.Println("ℹ️  No contributions found to import")
	}
}

func pushToServer(data []Contribution) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	resp, err := http.Post(LocalServerURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	return nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
