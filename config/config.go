package config

import (
	"log"
	"os"
	"strconv"
)

type AppConfig struct {
	TelegramUserID   int64
	Temperature      float32
	EntropyThreshold int

	GitHubSecret     string
	GitHubToken      string
	GroqModel        string
	GroqKey          string
	TelegramBotToken string
	Port             string
}

func LoadConfig() AppConfig {
	userID, err := strconv.ParseInt(os.Getenv("TELEGRAM_USER_ID"), 10, 64)
	if err != nil {
		log.Fatalf("CRITICAL: Invalid TELEGRAM_USER_ID: %v", err)
	}

	model := os.Getenv("GROQ_MODEL")
	if model == "" {
		model = "llama-3.3-70b-versatile"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	return AppConfig{
		Temperature:      getEnvAsFloat32("TEMPERATURE", 0.2),
		EntropyThreshold: getEnvAsInt("ENTROPY_THRESHOLD", 10),
		GitHubSecret:     os.Getenv("GITHUB_WEBHOOK_SECRET"),
		GitHubToken:      os.Getenv("GITHUB_PERSONAL_TOKEN"),
		GroqKey:          os.Getenv("GROQ_API_KEY"),
		GroqModel:        model,
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramUserID:   userID,
		Port:             port,
	}
}

func getEnvAsInt(name string, fallback int) int {
	if valStr, exists := os.LookupEnv(name); exists {
		if val, err := strconv.Atoi(valStr); err == nil {
			return val
		}
	}
	return fallback
}

func getEnvAsFloat32(name string, fallback float32) float32 {
	if valStr, exists := os.LookupEnv(name); exists {
		if val, err := strconv.ParseFloat(valStr, 32); err == nil {
			return float32(val)
		}
	}
	return fallback
}
