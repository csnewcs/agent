package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/bwmarrin/discordgo"
)

func buildStatsCommand() (BotCommand, error) {
	return NewBotCommandBuilder("stats").
		WithDescription(".").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{discordgo.ApplicationIntegrationUserInstall}).
		WithContexts(&[]discordgo.InteractionContextType{discordgo.InteractionContextGuild, discordgo.InteractionContextBotDM, discordgo.InteractionContextPrivateChannel}).
		WithFunction(handleStatsCommand).
		Build()
}

func handleStatsCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	var mu sync.Mutex
	serverStatus := "```\n측정 중...\n```"
	pingStatus := "```\n측정 중...\n```"
	antigravityStatus := "```\n측정 중...\n```"
	codexStatus := "```\n측정 중...\n```"
	tokenStatus := "```\n측정 중...\n```"

	buildComponents := func() []discordgo.MessageComponent {
		body := fmt.Sprintf("**[서버 자원 상태]**\n%s\n**[1.1.1.1 핑 상태]**\n%s\n**[Antigravity 쿼터]**\n%s\n**[Codex 쿼터]**\n%s\n**[OpenAI 토큰 사용량]**\n%s",
			serverStatus, pingStatus, antigravityStatus, codexStatus, tokenStatus)

		return NewComponentsBuilder().
			WithTitle("시스템 및 서비스 상태").
			WithBody(body).
			WithFooter("실시간 리소스 및 API 쿼터 측정").
			Build()
	}

	updateMessage := func() {
		mu.Lock()
		comps := buildComponents()
		mu.Unlock()

		_, err := EditInteractionComponentsV2(s, ic, comps)
		if err != nil {
			slog.Error("Failed to edit interaction response in stats", "error", err)
		}
	}

	// Respond initially
	err := RespondComponentsV2(s, ic, buildComponents(), false)
	if err != nil {
		slog.Error("Failed to respond to stats interaction", "error", err)
		return
	}

	// Run tasks in parallel
	go func() {
		res := collectServerStats()
		mu.Lock()
		serverStatus = res
		mu.Unlock()
		updateMessage()
	}()

	go func() {
		res := collectPingStats()
		mu.Lock()
		pingStatus = res
		mu.Unlock()
		updateMessage()
	}()

	go func() {
		res := collectAntigravityQuota()
		mu.Lock()
		antigravityStatus = res
		mu.Unlock()
		updateMessage()
	}()

	go func() {
		res := collectCodexQuota()
		mu.Lock()
		codexStatus = res
		mu.Unlock()
		updateMessage()
	}()

	go func() {
		res := collectOpenAITokens()
		mu.Lock()
		tokenStatus = res
		mu.Unlock()
		updateMessage()
	}()
}

func getCPUUsage() (float64, error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var user, nice, system, idle, iowait, irq, softirq, steal, guest, guestnice uint64
	var cpu string
	_, err = fmt.Fscanf(file, "%s %d %d %d %d %d %d %d %d %d %d", &cpu, &user, &nice, &system, &idle, &iowait, &irq, &softirq, &steal, &guest, &guestnice)
	if err != nil {
		return 0, err
	}

	idle1 := idle + iowait
	nonIdle1 := user + nice + system + irq + softirq + steal
	total1 := idle1 + nonIdle1

	time.Sleep(500 * time.Millisecond)

	file2, err := os.Open("/proc/stat")
	if err != nil {
		return 0, err
	}
	defer file2.Close()

	_, err = fmt.Fscanf(file2, "%s %d %d %d %d %d %d %d %d %d %d", &cpu, &user, &nice, &system, &idle, &iowait, &irq, &softirq, &steal, &guest, &guestnice)
	if err != nil {
		return 0, err
	}

	idle2 := idle + iowait
	nonIdle2 := user + nice + system + irq + softirq + steal
	total2 := idle2 + nonIdle2

	totalDiff := total2 - total1
	idleDiff := idle2 - idle1

	if totalDiff == 0 {
		return 0, nil
	}

	cpuPercentage := float64(totalDiff-idleDiff) / float64(totalDiff) * 100
	return cpuPercentage, nil
}

func getCPUTemp() (float64, error) {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 0, err
	}
	tempStr := strings.TrimSpace(string(data))
	var tempVal int
	if _, err := fmt.Sscan(tempStr, &tempVal); err != nil {
		return 0, err
	}
	return float64(tempVal) / 1000.0, nil
}

func getRAMUsage() (string, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	var memTotal, memAvailable uint64
	for _, line := range lines {
		if strings.HasPrefix(line, "MemTotal:") {
			_, _ = fmt.Sscanf(line, "MemTotal: %d kB", &memTotal)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			_, _ = fmt.Sscanf(line, "MemAvailable: %d kB", &memAvailable)
		}
	}
	if memTotal == 0 {
		return "", fmt.Errorf("could not parse MemTotal")
	}
	totalGB := float64(memTotal) / 1024.0 / 1024.0
	availableGB := float64(memAvailable) / 1024.0 / 1024.0
	usedGB := totalGB - availableGB
	usedPercent := usedGB / totalGB * 100
	return fmt.Sprintf("%.2f GB / %.2f GB (%.1f%%)", usedGB, totalGB, usedPercent), nil
}

func collectServerStats() string {
	cpu, err := getCPUUsage()
	cpuStr := "Error"
	if err == nil {
		cpuStr = fmt.Sprintf("%.1f%%", cpu)
	}

	temp, err := getCPUTemp()
	tempStr := "Error"
	if err == nil {
		tempStr = fmt.Sprintf("%.1f°C", temp)
	}

	ramStr, err := getRAMUsage()
	if err != nil {
		ramStr = "Error"
	}

	return fmt.Sprintf("```\nCPU Usage: %s\nCPU Temp : %s\nRAM Usage: %s\n```", cpuStr, tempStr, ramStr)
}

func collectPingStats() string {
	cmd := exec.Command("ping", "-c", "3", "-W", "2", "1.1.1.1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "```\nError: Connection failed\n```"
	}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, "min/avg/max") {
			parts := strings.Split(line, " = ")
			if len(parts) == 2 {
				raw := strings.TrimSpace(parts[1])
				raw = strings.TrimSuffix(raw, " ms")
				rtts := strings.Split(raw, "/")
				if len(rtts) >= 3 {
					return fmt.Sprintf("```\nAvg RTT: %s ms\nMin RTT: %s ms\nMax RTT: %s ms\n```", rtts[1], rtts[0], rtts[2])
				}
			}
		}
	}
	return "```\nError: Analysis failed\n```"
}

func formatNumber(n int) string {
	in := fmt.Sprintf("%d", n)
	if len(in) <= 3 {
		return in
	}
	var out []byte
	rem := len(in) % 3
	if rem > 0 {
		out = append(out, in[:rem]...)
		if len(in) > rem {
			out = append(out, ',')
		}
	}
	for i := rem; i < len(in); i += 3 {
		out = append(out, in[i:i+3]...)
		if i+3 < len(in) {
			out = append(out, ',')
		}
	}
	return string(out)
}

func formatResetTime(isoTime string) string {
	if isoTime == "" {
		return "N/A"
	}
	var t time.Time
	var err error
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		t, err = time.Parse(f, isoTime)
		if err == nil {
			break
		}
	}
	if err != nil {
		return isoTime
	}

	loc, lErr := time.LoadLocation("Asia/Seoul")
	if lErr != nil || loc == nil {
		loc = time.FixedZone("KST", 9*3600)
	}
	return t.In(loc).Format("2006-01-02 15:04 KST")
}

type QuotaGroup struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	Family           string  `json:"family"`
	RemainingPercent float64 `json:"remainingPercent"`
	ResetTime        string  `json:"resetTime"`
}

type QuotaData struct {
	UserTier struct {
		Name string `json:"name"`
	} `json:"userTier"`
	PlanInfo struct {
		PlanName string `json:"planName"`
	} `json:"planInfo"`
	PromptCredits struct {
		Available int     `json:"available"`
		Monthly   int     `json:"monthly"`
		Percent   float64 `json:"percent"`
	} `json:"promptCredits"`
	FlowCredits struct {
		Available int     `json:"available"`
		Monthly   int     `json:"monthly"`
		Percent   float64 `json:"percent"`
	} `json:"flowCredits"`
	QuotaGroups []QuotaGroup `json:"quotaGroups"`
}

type AntigravityQuotaResponse struct {
	Success     bool       `json:"success"`
	Error       string     `json:"error"`
	Antigravity *QuotaData `json:"antigravity"`
	UserStatus  *QuotaData `json:"userStatus"`
	UserTier    *struct {
		Name string `json:"name"`
	} `json:"userTier"`
	PlanInfo *struct {
		PlanName string `json:"planName"`
	} `json:"planInfo"`
	PromptCredits *struct {
		Available int     `json:"available"`
		Monthly   int     `json:"monthly"`
		Percent   float64 `json:"percent"`
	} `json:"promptCredits"`
	FlowCredits *struct {
		Available int     `json:"available"`
		Monthly   int     `json:"monthly"`
		Percent   float64 `json:"percent"`
	} `json:"flowCredits"`
	QuotaGroups []QuotaGroup `json:"quotaGroups"`
	Codex       *QuotaData   `json:"codex"`
}

func collectCodexQuota() string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:8091/api/quota")
	if err != nil {
		return fmt.Sprintf("```\nError: %s\n```", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("```\nError: HTTP %d\n```", resp.StatusCode)
	}
	var data AntigravityQuotaResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || data.Codex == nil {
		if err != nil {
			return fmt.Sprintf("```\nError: %s\n```", err.Error())
		}
		return "```\nError: Codex quota unavailable\n```"
	}

	plan := data.Codex.UserTier.Name
	if plan == "" {
		plan = "ChatGPT"
	}
	labelWidth := statsDisplayWidth("Plan")
	for _, group := range data.Codex.QuotaGroups {
		if width := statsDisplayWidth(group.Title); width > labelWidth {
			labelWidth = width
		}
	}
	formatLine := func(label, value string) string {
		padding := labelWidth - statsDisplayWidth(label) + 1
		return label + strings.Repeat(" ", padding) + ": " + value
	}

	lines := []string{formatLine("Plan", plan)}
	for _, group := range data.Codex.QuotaGroups {
		value := fmt.Sprintf("%.1f%%", group.RemainingPercent)
		if reset := formatResetTime(group.ResetTime); reset != "" && reset != "N/A" {
			value += " (Reset: " + reset + ")"
		}
		lines = append(lines, formatLine(group.Title, value))
	}
	if len(data.Codex.QuotaGroups) == 0 {
		lines = append(lines, formatLine("쿼터", "N/A"))
	}
	return "```\n" + strings.Join(lines, "\n") + "\n```"
}

// statsDisplayWidth returns the approximate monospace display width used by
// Discord code blocks. Hangul and other wide Unicode characters occupy two
// columns even though fmt's string width counts them as a single rune.
func statsDisplayWidth(value string) int {
	width := 0
	for _, r := range value {
		if r >= 0x1100 {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func collectAntigravityQuota() string {
	urls := []string{
		"http://127.0.0.1:8095/api/quota",
		"http://127.0.0.1:8090/api/quota",
		"http://localhost:8095/api/quota",
		"http://localhost:8090/api/quota",
	}

	client := &http.Client{Timeout: 3 * time.Second}
	var data AntigravityQuotaResponse
	var fetchErr error
	var success bool

	for _, u := range urls {
		resp, err := client.Get(u)
		if err != nil {
			fetchErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			fetchErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err == nil && (data.Antigravity != nil || data.PromptCredits != nil || data.UserStatus != nil || (data.Success && len(data.QuotaGroups) > 0)) {
			fetchErr = nil
			success = true
			break
		}
	}

	if !success {
		if fetchErr != nil {
			return fmt.Sprintf("```\nError: %s\n```", fetchErr.Error())
		}
		return "```\nError: Service unavailable\n```"
	}

	var qData QuotaData
	if data.Antigravity != nil {
		qData = *data.Antigravity
	} else if data.UserStatus != nil {
		qData = *data.UserStatus
	} else {
		if data.UserTier != nil {
			qData.UserTier = *data.UserTier
		}
		if data.PlanInfo != nil {
			qData.PlanInfo = *data.PlanInfo
		}
		if data.PromptCredits != nil {
			qData.PromptCredits = *data.PromptCredits
		}
		if data.FlowCredits != nil {
			qData.FlowCredits = *data.FlowCredits
		}
		qData.QuotaGroups = data.QuotaGroups
	}

	tierName := qData.UserTier.Name
	if tierName == "" {
		tierName = "Google AI Pro"
	}
	planName := qData.PlanInfo.PlanName
	if planName == "" {
		planName = "Pro"
	}

	var geminiStr, claudeStr string
	for _, q := range qData.QuotaGroups {
		var poolText string
		if q.RemainingPercent >= 100.0 {
			poolText = fmt.Sprintf("%.1f%%", q.RemainingPercent)
		} else {
			resetStr := formatResetTime(q.ResetTime)
			if resetStr != "N/A" && resetStr != "" {
				poolText = fmt.Sprintf("%.1f%% (Reset: %s)", q.RemainingPercent, resetStr)
			} else {
				poolText = fmt.Sprintf("%.1f%%", q.RemainingPercent)
			}
		}

		if q.ID == "gemini_pool" {
			geminiStr = poolText
		} else if q.ID == "claude_gpt_pool" {
			claudeStr = poolText
		}
	}

	if geminiStr == "" {
		geminiStr = "N/A"
	}
	if claudeStr == "" {
		claudeStr = "N/A"
	}

	return fmt.Sprintf("```\nTier/Plan  : %s (%s)\nGemini Pool: %s\nClaude/GPT : %s\n```",
		tierName, planName, geminiStr, claudeStr)
}

func collectOpenAITokens() string {
	apiKey := os.Getenv("OPENAI_ADMIN_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return "```\nError: OPENAI_ADMIN_KEY is not set in .env\n```"
	}

	now := time.Now()
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil || loc == nil {
		loc = time.FixedZone("KST", 9*3600)
	}
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	startTime := startOfToday.Unix()

	var promptTokens, completionTokens int
	url := fmt.Sprintf("https://api.openai.com/v1/organization/usage/completions?start_time=%d", startTime)

	client := &http.Client{Timeout: 10 * time.Second}

	for url != "" {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "```\nError: Failed to create request: " + err.Error() + "\n```"
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return "```\nError: API call failed: " + err.Error() + "\n```"
		}

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Sprintf("```\nAPI Error (Status %d): %s\n```", resp.StatusCode, string(bodyBytes))
		}

		var response struct {
			Data []struct {
				StartTime int                      `json:"start_time"`
				EndTime   int                      `json:"end_time"`
				Results   []map[string]interface{} `json:"results"`
			} `json:"data"`
			HasMore  bool    `json:"has_more"`
			NextPage *string `json:"next_page"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			resp.Body.Close()
			return "```\nError: JSON parse failed: " + err.Error() + "\n```"
		}
		resp.Body.Close()

		for _, bucket := range response.Data {
			if bucket.EndTime <= int(startTime) {
				continue
			}
			for _, result := range bucket.Results {
				for k, v := range result {
					valFloat, ok := v.(float64)
					if !ok {
						continue
					}
					val := int(valFloat)
					if k == "input_tokens" {
						promptTokens += val
					} else if k == "output_tokens" {
						completionTokens += val
					}
				}
			}
		}

		if response.HasMore && response.NextPage != nil && *response.NextPage != "" {
			url = fmt.Sprintf("https://api.openai.com/v1/organization/usage/completions?start_time=%d&page=%s", startTime, *response.NextPage)
		} else {
			url = ""
		}
	}

	totalTokens := promptTokens + completionTokens
	percentage := (float64(totalTokens) / 2500000.0) * 100.0

	return fmt.Sprintf("```\nPrompt    : %s tokens\nCompletion: %s tokens\nTotal     : %s tokens (%.2f%%)\n```",
		formatNumber(promptTokens), formatNumber(completionTokens), formatNumber(totalTokens), percentage)
}
