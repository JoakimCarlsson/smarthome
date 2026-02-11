package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/joakimcarlsson/ai/agent"
	"github.com/joakimcarlsson/ai/model"
	llm "github.com/joakimcarlsson/ai/providers"
	"github.com/joakimcarlsson/ai/transcription"
	"github.com/joakimcarlsson/ai/types"
	"github.com/joakimcarlsson/smarthome/internal/audio"
	"github.com/joakimcarlsson/smarthome/internal/config"
	"github.com/joakimcarlsson/smarthome/internal/otel"
	"github.com/joakimcarlsson/smarthome/internal/tools"
	"github.com/joakimcarlsson/smarthome/internal/tts"
)

const (
	serviceName    = "smarthome"
	serviceVersion = "0.1.0"
)

//go:embed prompts/system.md
var systemPrompt string

func main() {
	cfg, err := config.Load(".env")
	if err != nil {
		slog.Error("loading config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	otelShutdown, err := otel.Setup(ctx, otel.Config{
		ServiceName:    serviceName,
		ServiceVersion: serviceVersion,
		OTLPEndpoint:   cfg.OTLPEndpoint,
		OTLPToken:      cfg.OTLPToken,
	})
	if err != nil {
		slog.Error("setting up otel", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			slog.Error("shutting down otel", "error", err)
		}
	}()

	otel.SetupLogger(serviceName, cfg.LogLevel, cfg.LogFormat)

	slog.Info("starting", "service", serviceName, "version", serviceVersion)

	mic, err := audio.New()
	if err != nil {
		slog.Error("creating audio capture", "error", err)
		os.Exit(1)
	}

	utterances, err := mic.Start(ctx)
	if err != nil {
		slog.Error("starting audio capture", "error", err)
		os.Exit(1)
	}
	defer mic.Close()

	stt, err := transcription.NewSpeechToText(
		model.ProviderOpenAI,
		transcription.WithModel(model.TranscriptionModel{
			APIModel: cfg.WhisperModel,
		}),
		transcription.WithOpenAIOptions(
			transcription.WithOpenAIBaseURL(cfg.WhisperURL),
		),
	)
	if err != nil {
		slog.Error("creating stt client", "error", err)
		os.Exit(1)
	}

	llamaModel := model.NewCustomModel(
		model.WithModelID("llama3.2"),
		model.WithAPIModel(cfg.LLMModel),
	)

	ollama := llm.RegisterCustomProvider("ollama", llm.CustomProviderConfig{
		BaseURL:      cfg.LLMURL,
		DefaultModel: llamaModel,
	})

	llmClient, err := llm.NewLLM(ollama)
	if err != nil {
		slog.Error("creating llm client", "error", err)
		os.Exit(1)
	}

	// webSearchTool := tools.NewWebSearchTool(cfg.SerpAPIKey)
	// res, err := webSearchTool.Run(ctx, tool.ToolCall{
	// 	Input: "What is the capital of France?",
	// 	Name:  "web_search",
	// 	ID:    "123",
	// })
	// if err != nil {
	// 	slog.Error("running web search tool", "error", err)
	// 	os.Exit(1)
	// }
	// fmt.Println(res.Content)

	myAgent := agent.New(llmClient,
		agent.WithSystemPrompt(systemPrompt),
		agent.WithTools(tools.NewWebSearchTool(cfg.SerpAPIKey)),
	)

	speaker, err := audio.NewPlayback()
	if err != nil {
		slog.Error("creating audio playback", "error", err)
		os.Exit(1)
	}
	defer speaker.Close()

	ttsConfig := tts.SessionConfig{
		APIKey:       cfg.ElevenLabsAPIKey,
		VoiceID:      cfg.ElevenLabsVoiceID,
		ModelID:      cfg.ElevenLabsModel,
		OutputFormat: "pcm_24000",
		Stability:    cfg.ElevenLabsStability,
		Similarity:   cfg.ElevenLabsSimilarity,
		Speed:        cfg.ElevenLabsSpeed,
	}

	slog.Info("listening for speech",
		"whisper_url", cfg.WhisperURL,
		"llm_url", cfg.LLMURL,
		"llm_model", cfg.LLMModel,
	)

	for pcm := range utterances {
		wav := audio.EncodeWAV(pcm, audio.DefaultSampleRate, 1, 16)

		var wsSession *tts.Session
		var wsErr error
		wsDone := make(chan struct{})
		go func() {
			wsSession, wsErr = tts.NewSession(ctx, ttsConfig)
			close(wsDone)
		}()

		resp, err := stt.Transcribe(ctx, wav,
			transcription.WithLanguage("sv"),
			transcription.WithFilename("audio.wav"),
		)
		if err != nil {
			slog.Error("transcribing", "error", err)
			<-wsDone
			if wsSession != nil {
				wsSession.Close()
			}
			continue
		}

		text := strings.TrimSpace(resp.Text)
		if text == "" {
			<-wsDone
			if wsSession != nil {
				wsSession.Close()
			}
			continue
		}

		slog.Info("transcribed", "text", text)

		<-wsDone
		if wsErr != nil {
			slog.Error("creating ws session", "error", wsErr)
			continue
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunk := range wsSession.Audio() {
				if chunk.Error != nil {
					slog.Error("tts chunk", "error", chunk.Error)
					return
				}
				if chunk.Done {
					break
				}
				if err := speaker.Play(chunk.Data); err != nil {
					slog.Error("playing audio", "error", err)
					return
				}
			}
			if err := speaker.Flush(); err != nil {
				slog.Error("flushing audio", "error", err)
			}
		}()

		for event := range myAgent.ChatStream(ctx, text) {
			switch event.Type {
			case types.EventContentDelta:
				fmt.Print(event.Content)
				if err := wsSession.SendText(event.Content); err != nil {
					slog.Error("sending text to tts", "error", err)
				}
			case types.EventError:
				slog.Error("agent stream", "error", event.Error)
			}
		}
		fmt.Println()

		if err := wsSession.Flush(); err != nil {
			slog.Error("flushing ws session", "error", err)
		}
		wg.Wait()
		wsSession.Close()
	}

	slog.Info("shutting down")
}
