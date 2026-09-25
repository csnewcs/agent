package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

type ProxyFileAttachment struct {
	Name          string `json:"name"`
	URL           string `json:"url,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Size          int    `json:"size,omitempty"`
}

type ProxyRequest struct {
	SessionID string                `json:"sessionId"`
	ProjectID string                `json:"projectId"`
	Prompt    string                `json:"prompt"`
	TurnID    string                `json:"turnId"`
	Model     string                `json:"model,omitempty"`
	Files     []ProxyFileAttachment `json:"files,omitempty"`
}

type ProxyResponse struct {
	SessionID string `json:"sessionId"`
	Type      string `json:"type"` // thinking, tool, chat, permission_request, completed, cancelled, error
	Tool      string `json:"tool"`
	Output    string `json:"output"`
}

type TokenUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

type ProxySessionInfo struct {
	SessionID     string     `json:"sessionId"`
	ProjectID     string     `json:"projectId"`
	TurnID        string     `json:"turnId"`
	Model         string     `json:"model,omitempty"`
	Prompt        string     `json:"prompt"`
	Status        string     `json:"status"` // running, completed, error, cancelled
	Tools         []string   `json:"tools"`
	Thinking      []string   `json:"thinking"`
	Chat          []string   `json:"chat"`
	FinalResponse string     `json:"finalResponse"`
	ErrorMessage  string     `json:"errorMessage"`
	IsRunning     bool       `json:"isRunning"`
	Usage         TokenUsage `json:"usage"`
}

type AntigravitySessionState struct {
	mu               sync.Mutex
	SessionID        string
	ProjectID        string
	TurnID           string
	Model            string
	UserID           string
	AppID            string
	InteractionToken string
	Title            string
	Prompt           string
	ChannelID        string
	MessageID        string
	Status           string // running, waiting_approval, completed, cancelled, error
	ThinkingLogs     []string
	ChatLogs         []string
	FinalResponse    string
	RawToolNames     []string
	PermissionTool   string
	ErrorMessage     string
	Usage            TokenUsage
	CancelFunc       func()
}

var (
	antigravitySessionsMu sync.RWMutex
	antigravitySessions   = make(map[string]*AntigravitySessionState)
)

const ProxyURL = "http://localhost:8090"

func InitAntigravityProxy(proxyPath string) error {
	slog.Info("Antigravity HTTP REST Proxy initialized", "url", ProxyURL)
	return nil
}

func RegisterAntigravitySession(state *AntigravitySessionState) {
	antigravitySessionsMu.Lock()
	defer antigravitySessionsMu.Unlock()
	antigravitySessions[state.SessionID] = state
}

func GetAntigravitySession(sessionID string) *AntigravitySessionState {
	antigravitySessionsMu.RLock()
	defer antigravitySessionsMu.RUnlock()
	return antigravitySessions[sessionID]
}

func GetAllAntigravitySessions() []*AntigravitySessionState {
	antigravitySessionsMu.RLock()
	defer antigravitySessionsMu.RUnlock()
	list := make([]*AntigravitySessionState, 0, len(antigravitySessions))
	for _, s := range antigravitySessions {
		list = append(list, s)
	}
	return list
}

func FetchProxySessionStatus(sessionID string) (*ProxySessionInfo, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("empty sessionID")
	}
	resp, err := http.Get(fmt.Sprintf("%s/api/session?sessionId=%s", ProxyURL, sessionID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy session returned status %d", resp.StatusCode)
	}

	var info ProxySessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func FetchAllProxySessions() ([]*ProxySessionInfo, error) {
	resp, err := http.Get(fmt.Sprintf("%s/api/sessions", ProxyURL))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy returned status %d", resp.StatusCode)
	}

	var res struct {
		Sessions []*ProxySessionInfo `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Sessions, nil
}

func SendAntigravityChatStream(sessionID, projectID, prompt, turnID, model string, files []ProxyFileAttachment) (<-chan ProxyResponse, func(), error) {
	if model == "" {
		model = "gemini-3.8-flash-high"
	}
	reqBody := ProxyRequest{
		SessionID: sessionID,
		ProjectID: projectID,
		Prompt:    prompt,
		TurnID:    turnID,
		Model:     model,
		Files:     files,
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, err
	}

	httpReq, err := http.NewRequest("POST", fmt.Sprintf("%s/api/chat", ProxyURL), bytes.NewBuffer(jsonBytes))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, nil, fmt.Errorf("proxy returned status %d: %s", resp.StatusCode, string(body))
	}

	outChan := make(chan ProxyResponse, 100)

	cancelFunc := func() {
		cancelReq := ProxyRequest{
			SessionID: sessionID,
			TurnID:    turnID,
		}
		cBytes, _ := json.Marshal(cancelReq)
		_, _ = http.Post(fmt.Sprintf("%s/api/cancel", ProxyURL), "application/json", bytes.NewBuffer(cBytes))
		resp.Body.Close()
	}

	go func() {
		defer close(outChan)
		defer resp.Body.Close()

		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				line = strings.TrimSpace(line)
				if line != "" {
					var pResp ProxyResponse
					if jsonErr := json.Unmarshal([]byte(line), &pResp); jsonErr == nil {
						outChan <- pResp
					}
				}
			}
			if err != nil {
				break
			}
		}
	}()

	return outChan, cancelFunc, nil
}

func ClearAntigravityProxySession(sessionID string) {
	reqBody := ProxyRequest{
		SessionID: sessionID,
	}
	jsonBytes, _ := json.Marshal(reqBody)
	_, _ = http.Post(fmt.Sprintf("%s/api/clear", ProxyURL), "application/json", bytes.NewBuffer(jsonBytes))
}

func CompactAntigravityProxySession(sessionID, projectID string) (string, error) {
	reqBody := ProxyRequest{
		SessionID: sessionID,
		ProjectID: projectID,
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	resp, err := http.Post(fmt.Sprintf("%s/api/compact", ProxyURL), "application/json", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("compact failed (%d): %s", resp.StatusCode, string(b))
	}

	var res struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.Summary, nil
}

func FetchProxyProjects() []string {
	resp, err := http.Get(fmt.Sprintf("%s/api/projects", ProxyURL))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var res struct {
		Projects []string `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil
	}
	return res.Projects
}
