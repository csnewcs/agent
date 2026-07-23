package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type KepcoUsageResponse struct {
	Customer struct {
		ElectricityRateName string `json:"electricityRateName"`
	} `json:"customer"`
	Period struct {
		BillingPeriodStartDate string  `json:"billingPeriodStartDate"`
		BillingPeriodEndDate   string  `json:"billingPeriodEndDate"`
		ElapsedBillingDays     float64 `json:"elapsedBillingDays"`
		BillingCycleDays       float64 `json:"billingCycleDays"`
	} `json:"period"`
	Usage struct {
		RealtimeKwh   float64 `json:"realtimeKwh"`
		PredictedKwh  float64 `json:"predictedKwh"`
		CurrentTier   float64 `json:"currentTier"`
		PredictedTier float64 `json:"predictedTier"`
	} `json:"usage"`
	Billing struct {
		RealtimeTotalCharge float64 `json:"realtimeTotalCharge"`
	} `json:"billing"`
	Predicted struct {
		TotalCharge float64 `json:"totalCharge"`
	} `json:"predicted"`
}

func formatComma(num float64) string {
	str := fmt.Sprintf("%.0f", num)
	length := len(str)
	if length <= 3 {
		return str
	}
	var result []string
	for i := length; i > 0; i -= 3 {
		start := i - 3
		if start < 0 {
			start = 0
		}
		result = append([]string{str[start:i]}, result...)
	}
	return strings.Join(result, ",")
}

func collectKepcoUsage() string {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get("http://localhost:4000/api/kepco/usage")
	if err != nil {
		return "```\nError: Failed to request KEPCO API: " + err.Error() + "\n```"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("```\nAPI Error (Status %d): %s\n```", resp.StatusCode, string(bodyBytes))
	}

	var data KepcoUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "```\nError: JSON parse failed: " + err.Error() + "\n```"
	}

	rateName := data.Customer.ElectricityRateName
	if rateName == "" {
		rateName = "알 수 없음"
	}

	startDate := data.Period.BillingPeriodStartDate
	endDate := data.Period.BillingPeriodEndDate
	elapsedDays := int(data.Period.ElapsedBillingDays)
	cycleDays := int(data.Period.BillingCycleDays)

	rtKwh := data.Usage.RealtimeKwh
	predKwh := data.Usage.PredictedKwh
	rtTier := int(data.Usage.CurrentTier)
	predTier := int(data.Usage.PredictedTier)

	rtCharge := data.Billing.RealtimeTotalCharge
	predCharge := data.Predicted.TotalCharge

	var sb strings.Builder
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("요금제      : %s\n", rateName))
	sb.WriteString(fmt.Sprintf("검침 주기   : %s ~ %s (%d일/%d일 경과)\n\n", startDate, endDate, elapsedDays, cycleDays))
	sb.WriteString("[실시간 사용 현황]\n")
	sb.WriteString(fmt.Sprintf("현재 사용량 : %.1f kWh (누진 %d단계)\n", rtKwh, rtTier))
	sb.WriteString(fmt.Sprintf("실시간 요금 : %s 원\n\n", formatComma(rtCharge)))
	sb.WriteString("[이번 달 예상 현황]\n")
	sb.WriteString(fmt.Sprintf("예상 사용량 : %.1f kWh (누진 %d단계)\n", predKwh, predTier))
	sb.WriteString(fmt.Sprintf("예상 총요금 : %s 원\n", formatComma(predCharge)))
	sb.WriteString("```")

	return sb.String()
}
