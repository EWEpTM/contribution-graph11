// Package lib 供 git-watch / github-import / gitlab-import 三个 agent 共享，
// 消除重复的 Contribution 结构、环境变量读取、文本截断与推送逻辑。
package lib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Contribution 统一事件结构：三个 agent 与服务器 API 共用的数据契约。
// Timestamp 使用 time.Time（JSON 序列化为 RFC3339），避免各 agent 自行格式化不一致。
type Contribution struct {
	Source    string                 `json:"source"`
	Context   string                 `json:"context"`
	Timestamp time.Time              `json:"timestamp"`
	MetaData  map[string]interface{} `json:"metadata,omitempty"`
}

// GetEnv 读取环境变量，为空时返回默认值
func GetEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// Truncate 按字符（rune）截断并追加省略号，避免切断多字节 UTF-8 产生乱码。
// maxRunes 过小时只返回省略号，保证不会越界 panic。
func Truncate(s string, maxRunes int) string {
	if maxRunes <= 3 {
		return "..."
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-3]) + "..."
}

// Push 将贡献批量推送到 Keep 服务器 /api/contributions 端点。
// 非 2xx 响应返回错误（保留原有 agent 的失败语义）。
func Push(endpoint string, contribs []Contribution) error {
	jsonData, err := json.Marshal(contribs)
	if err != nil {
		return err
	}

	url := strings.TrimSuffix(endpoint, "/") + "/api/contributions"
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}
	return nil
}

// SpreadSeconds 将单日 count 条贡献按当天秒数均匀展开，返回时间戳偏移序列长度 count。
// 旧实现按"每分钟一条"展开，单日超过 1440 条会溢出到相邻日期污染日历；
// 按秒均分后单日最多 86400 条也不会跨天。极端超限时截断到 86400 条并保留尾部。
func SpreadSeconds(base time.Time, count int) []time.Time {
	if count <= 0 {
		return nil
	}
	if count > 86400 {
		count = 86400
	}
	step := 86400 / count
	if step < 1 {
		step = 1
	}
	out := make([]time.Time, count)
	for i := 0; i < count; i++ {
		out[i] = base.Add(time.Duration(i*step) * time.Second)
	}
	return out
}
