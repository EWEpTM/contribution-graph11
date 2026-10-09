package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Tomer-Barak/contribution-graph/agents/lib"
)

// Configuration - Set these via environment variables
var (
	GitHubUsername = os.Getenv("GITHUB_USERNAME")
	GitHubToken    = os.Getenv("GITHUB_TOKEN")
	LocalServerURL = lib.GetEnv("SERVER_URL", "http://localhost:8080")
)

// GitHub GraphQL Query（用户名经 graphqlEscape 转义后填充，防止引号/反斜杠破坏查询）
const query = `
{
  user(login: "%s") {
    contributionsCollection {
      contributionCalendar {
        totalContributions
        weeks {
          contributionDays {
            date
            contributionCount
          }
        }
      }
    }
  }
}
`

// Structs to parse GitHub Response
type GitHubResponse struct {
	Data struct {
		User struct {
			ContributionsCollection struct {
				ContributionCalendar struct {
					TotalContributions int `json:"totalContributions"`
					Weeks              []struct {
						ContributionDays []struct {
							Date              string `json:"date"`
							ContributionCount int    `json:"contributionCount"`
						} `json:"contributionDays"`
					} `json:"weeks"`
				} `json:"contributionCalendar"`
			} `json:"contributionsCollection"`
		} `json:"user"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// graphqlEscape 转义 GraphQL 字符串字面量中的引号与反斜杠
func graphqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

func main() {
	// Validate configuration
	if GitHubUsername == "" {
		fmt.Println("❌ Error: GITHUB_USERNAME environment variable is required")
		fmt.Println("\nUsage:")
		fmt.Println("  export GITHUB_USERNAME=your-username")
		fmt.Println("  export GITHUB_TOKEN=your-personal-access-token")
		fmt.Println("  go run main.go")
		os.Exit(1)
	}

	if GitHubToken == "" {
		fmt.Println("❌ Error: GITHUB_TOKEN environment variable is required")
		fmt.Println("\nCreate a Personal Access Token at:")
		fmt.Println("  https://github.com/settings/tokens")
		fmt.Println("  (Select 'read:user' scope)")
		os.Exit(1)
	}

	fmt.Printf("🚀 Fetching contribution data from GitHub for user: %s\n", GitHubUsername)

	// Prepare the GraphQL Request
	q := fmt.Sprintf(query, graphqlEscape(GitHubUsername))
	reqBody, _ := json.Marshal(map[string]string{"query": q})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewBuffer(reqBody))
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", "Bearer "+GitHubToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "contribution-graph-importer")

	// Send Request to GitHub
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Error fetching from GitHub: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Printf("❌ GitHub API returned status %d\n", resp.StatusCode)
		os.Exit(1)
	}

	// Parse Response
	var ghResp GitHubResponse
	if err := json.NewDecoder(resp.Body).Decode(&ghResp); err != nil {
		fmt.Printf("❌ Error parsing JSON: %v\n", err)
		os.Exit(1)
	}

	if len(ghResp.Errors) > 0 {
		fmt.Printf("❌ GitHub API Error: %s\n", ghResp.Errors[0].Message)
		os.Exit(1)
	}

	// Convert to Your App's Format
	var myContributions []lib.Contribution
	calendar := ghResp.Data.User.ContributionsCollection.ContributionCalendar
	importDate := time.Now().Format(time.RFC3339)

	fmt.Printf("📊 Total contributions in the last year: %d\n", calendar.TotalContributions)

	for _, week := range calendar.Weeks {
		for _, day := range week.ContributionDays {
			if day.ContributionCount == 0 {
				continue
			}

			// Parse date (YYYY-MM-DD)
			t, err := time.Parse("2006-01-02", day.Date)
			if err != nil {
				continue
			}

			// 单日贡献按秒均匀展开（与 gitlab-import 一致）：旧实现按"每分钟一条"，
			// 单日超过 1440 条会溢出到相邻日期污染日历
			for _, ts := range lib.SpreadSeconds(t, day.ContributionCount) {
				myContributions = append(myContributions, lib.Contribution{
					Source:    "github-import",
					Context:   "github-history",
					Timestamp: ts,
					MetaData: map[string]interface{}{
						"imported_from_github": true,
						"original_count":       day.ContributionCount,
						"import_date":          importDate,
					},
				})
			}
		}
	}

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
		fmt.Println("🎉 Successfully imported GitHub history to your local app!")
	}
}
