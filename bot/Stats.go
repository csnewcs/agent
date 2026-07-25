package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

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

func collectOpenAITokens() string {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "```\nError: OPENAI_API_KEY is not set in .env\n```"
	}

	// Calculate start of today in local time
	now := time.Now()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startTime := startOfToday.Unix()

	var promptTokens, completionTokens int
	url := fmt.Sprintf("https://api.openai.com/v1/organization/usage/completions?start_time=%d", startTime)

	for url != "" {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "```\nError: Failed to create request: " + err.Error() + "\n```"
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return "```\nError: API call failed: " + err.Error() + "\n```"
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return fmt.Sprintf("```\nAPI Error (Status %d): %s\n```", resp.StatusCode, string(bodyBytes))
		}

		var response struct {
			Data []struct {
				Results []map[string]interface{} `json:"results"`
			} `json:"data"`
			HasMore  bool    `json:"has_more"`
			NextPage *string `json:"next_page"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			return "```\nError: JSON parse failed: " + err.Error() + "\n```"
		}

		for _, bucket := range response.Data {
			for _, result := range bucket.Results {
				for k, v := range result {
					valFloat, ok := v.(float64)
					if !ok {
						continue
					}
					val := int(valFloat)
					if k == "input_tokens" || k == "prompt_tokens" {
						promptTokens += val
					} else if k == "output_tokens" || k == "completion_tokens" {
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

	return fmt.Sprintf("```\nPrompt    : %d tokens\nCompletion: %d tokens\nTotal     : %d tokens (%.2f%%)\n```", promptTokens, completionTokens, totalTokens, percentage)
}
