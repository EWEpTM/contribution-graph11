package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Tomer-Barak/contribution-graph/agents/lib"
)

// Configuration - Set these via environment variables
var (
	GitLabUsername = os.Getenv("GITLAB_USERNAME")
	GitLabToken    = os.Getenv("GITLAB_TOKEN")
	GitLabHost     = lib.GetEnv("GITLAB_HOST", "gitlab.com")
	LocalServerURL = lib.GetEnv("SERVER_URL", "http://localhost:8080")
	// AgentTZ 事件分组与展开使用的时区：GitLab 事件时间为 UTC，
	// 若按 UTC 日期分组而服务器按本地时区展示，UTC 16:00 后的事件会归属"本地次日"，
	// 造成日历日期与 GitLab 显示差一天。默认与服务器 TZ 一致（Asia/Shanghai）。
	AgentTZ = lib.GetEnv("TZ", "Asia/Shanghai")
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

	loc, err := time.LoadLocation(AgentTZ)
	if err != nil {
		fmt.Printf("❌ Invalid TZ %q: %v\n", AgentTZ, err)
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

	// Group contributions by date（按 AgentTZ 时区的本地日期，与服务器展示一致）
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
			case "created", "updated", "closed", "reopened", "merged", "opened",
				"accepted", "approved", "commented on", "deleted", "imported", "moved":
				count = 1
			}

			if count > 0 {
				date := event.CreatedAt.In(loc).Format("2006-01-02")
				contributionsByDate[date] += count
			}
		}

		page++
		if page > 100 {
			break
		}
	}

	// Convert to Your App's Format
	var myContributions []lib.Contribution
	importDate := time.Now().Format(time.RFC3339)
	totalContributions := 0

	for dateStr, count := range contributionsByDate {
		totalContributions += count

		// Parse date (YYYY-MM-DD) 并构造 AgentTZ 时区的当天零点
		day, err := time.ParseInLocation("2006-01-02", dateStr, loc)
		if err != nil {
			continue
		}
		dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)

		// 单日贡献按当天总秒数均分：单日超过 86400 条由 lib 截断，不会溢出到相邻日期
		for _, ts := range lib.SpreadSeconds(dayStart, count) {
			myContributions = append(myContributions, lib.Contribution{
				Source:    "gitlab-import",
				Context:   "gitlab-history",
				Timestamp: ts,
				MetaData: map[string]interface{}{
					"imported_from_gitlab": true,
					"original_count":       count,
					"import_date":          importDate,
					"gitlab_host":          GitLabHost,
				},
			})
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
		err := lib.Push(LocalServerURL, myContributions)
		if err != nil {
			fmt.Printf("❌ Error uploading: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("🎉 Successfully imported GitLab history to your local app!")
	} else {
		fmt.Println("ℹ️  No contributions found to import")
	}
}
