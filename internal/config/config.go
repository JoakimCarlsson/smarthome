package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	LogLevel  string
	LogFormat string

	OTLPEndpoint string
	OTLPToken    string

	OpenAIAPIKey string

	AnthropicAPIKey string

	SerpAPIKey string

	PicovoiceAccessKey string

	ElevenLabsAPIKey     string
	ElevenLabsVoiceID    string
	ElevenLabsModel      string
	ElevenLabsStability  float64
	ElevenLabsSimilarity float64
	ElevenLabsSpeed      float64
}

func Load(envFile string) (*Config, error) {
	if err := godotenv.Load(envFile); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	config := &Config{
		LogLevel:     getEnv("LOG_LEVEL", "info"),
		LogFormat:    getEnv("LOG_FORMAT", "json"),
		OTLPEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTLPToken:    getEnv("OTEL_EXPORTER_OTLP_TOKEN", ""),
		OpenAIAPIKey: getEnv("OPENAI_API_KEY", ""),

		AnthropicAPIKey: getEnv("ANTHROPIC_API_KEY", ""),

		SerpAPIKey: getEnv("SERPAPI_KEY", ""),

		PicovoiceAccessKey: getEnv("PICOVOICE_ACCESS_KEY", ""),

		ElevenLabsAPIKey:     getEnv("ELEVENLABS_API_KEY", ""),
		ElevenLabsVoiceID:    getEnv("ELEVENLABS_VOICE_ID", "aSLKtNoVBZlxQEMsnGL2"),
		ElevenLabsModel:      getEnv("ELEVENLABS_MODEL", "eleven_flash_v2_5"),
		ElevenLabsStability:  getEnvAsFloat("ELEVENLABS_STABILITY", 0.5),
		ElevenLabsSimilarity: getEnvAsFloat("ELEVENLABS_SIMILARITY", 0.8),
		ElevenLabsSpeed:      getEnvAsFloat("ELEVENLABS_SPEED", 1.20),
	}

	return config, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsSlice(key string, defaultValue []string) []string {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}
	return strings.Split(valueStr, ",")
}

func getEnvAsFloat(key string, defaultValue float64) float64 {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return defaultValue
	}
	return value
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}
