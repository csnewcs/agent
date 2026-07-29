package main

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Token            string
	Mode             string
	DefaultServerID  string
	DefaultChannelID string
	DBURL            string
	TJDBURL          string
	N8NWebhookURL    string
	TestWebhookURL   string
}

func LoadConfig(path string) (*Config, error) {
	err := godotenv.Load(path)
	if err != nil {
		return nil, err
	}
	conf := &Config{
		Token:            getEnv("TOKEN", ""),
		Mode:             getEnv("MODE", "development"),
		DefaultServerID:  getEnv("DEFAULT_SERVER_ID", ""),
		DefaultChannelID: getEnv("DEFAULT_CHANNEL_ID", ""),
		DBURL:            getEnv("DATABASE_URL", "postgres://agent@localhost:5432/agent?sslmode=disable"),
		TJDBURL:          getEnv("TJ_DATABASE_URL", "postgresql://agent@localhost:5432/tj?sslmode=disable"),
		N8NWebhookURL:    getEnv("N8N_WEBHOOK_URL", ""),
		TestWebhookURL:   getEnv("N8N_TEST_WEBHOOK_URL", ""),
	}
	if conf.Token == "" {
		return nil, fmt.Errorf("TOKEN is not set")
	}
	if conf.DefaultChannelID == "" {
		return nil, fmt.Errorf("DEFAULT_CHANNEL_ID is not set")
	}
	if conf.Mode == "development" && conf.DefaultServerID == "" {
		return nil, fmt.Errorf("DEFAULT_SERVER_ID is not set in development mode. Slash commands will not be registered.")
	}
	if conf.Mode == "development" {
		if conf.TestWebhookURL == "" && conf.N8NWebhookURL == "" {
			return nil, fmt.Errorf("neither N8N_TEST_WEBHOOK_URL nor N8N_WEBHOOK_URL is set in development mode")
		}
	} else {
		if conf.N8NWebhookURL == "" {
			return nil, fmt.Errorf("N8N_WEBHOOK_URL is not set")
		}
	}
	return conf, nil
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}
