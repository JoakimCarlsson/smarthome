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

	WhisperURL   string
	WhisperModel string

	LLMURL   string
	LLMModel string

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
		WhisperURL:   getEnv("WHISPER_URL", "http://192.168.1.217:11435/v1"),
		WhisperModel: getEnv("WHISPER_MODEL", "Systran/faster-whisper-small"),
		LLMURL:       getEnv("LLM_URL", "http://192.168.1.217:11434/v1"),
		LLMModel:     getEnv("LLM_MODEL", "llama3.2:3b"),

		ElevenLabsAPIKey:     getEnv("ELEVENLABS_API_KEY", "sk_0f2a7a7c78e35688600cdcf6bc5b6c64516d23dd0b599443"),
		ElevenLabsVoiceID:    getEnv("ELEVENLABS_VOICE_ID", "2ajXGJNYBR0iNHpS4VZb"),
		ElevenLabsModel:      getEnv("ELEVENLABS_MODEL", "eleven_multilingual_v2"),
		ElevenLabsStability:  getEnvAsFloat("ELEVENLABS_STABILITY", 0.5),
		ElevenLabsSimilarity: getEnvAsFloat("ELEVENLABS_SIMILARITY", 0.8),
		ElevenLabsSpeed:      getEnvAsFloat("ELEVENLABS_SPEED", 1.0),
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
