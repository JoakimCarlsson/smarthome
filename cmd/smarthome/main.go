package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/joakimcarlsson/ai/message"
	"github.com/joakimcarlsson/ai/model"
	llm "github.com/joakimcarlsson/ai/providers"
	"github.com/joakimcarlsson/ai/transcription"
	"github.com/joakimcarlsson/ai/types"
	"github.com/joakimcarlsson/smarthome/internal/audio"
	"github.com/joakimcarlsson/smarthome/internal/config"
	"github.com/joakimcarlsson/smarthome/internal/otel"
	"github.com/joakimcarlsson/smarthome/internal/tts"
)

const (
	serviceName    = "smarthome"
	serviceVersion = "0.1.0"
)

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

	speaker, err := audio.NewPlayback()
	if err != nil {
		slog.Error("creating audio playback", "error", err)
		os.Exit(1)
	}
	defer speaker.Close()

	conversation := []message.Message{
		message.NewSystemMessage("You are a helpful smart home assistant. Keep responses concise and conversational."),
	}

	slog.Info("listening for speech",
		"whisper_url", cfg.WhisperURL,
		"llm_url", cfg.LLMURL,
		"llm_model", cfg.LLMModel,
	)

	for pcm := range utterances {
		wav := audio.EncodeWAV(pcm, audio.DefaultSampleRate, 1, 16)

		resp, err := stt.Transcribe(ctx, wav,
			transcription.WithLanguage("sv"),
			transcription.WithFilename("audio.wav"),
		)
		if err != nil {
			slog.Error("transcribing", "error", err)
			continue
		}

		text := strings.TrimSpace(resp.Text)
		if text == "" {
			continue
		}

		slog.Info("transcribed", "text", text)

		conversation = append(conversation, message.NewUserMessage(text))

		wsSession, err := tts.NewSession(ctx, tts.SessionConfig{
			APIKey:       cfg.ElevenLabsAPIKey,
			VoiceID:      cfg.ElevenLabsVoiceID,
			ModelID:      cfg.ElevenLabsModel,
			OutputFormat: "pcm_24000",
			Stability:    cfg.ElevenLabsStability,
			Similarity:   cfg.ElevenLabsSimilarity,
			Speed:        cfg.ElevenLabsSpeed,
		})
		if err != nil {
			slog.Error("creating ws session", "error", err)
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

		var reply strings.Builder
		stream := llmClient.StreamResponse(ctx, conversation, nil)

		for event := range stream {
			switch event.Type {
			case types.EventContentDelta:
				fmt.Print(event.Content)
				reply.WriteString(event.Content)
				if err := wsSession.SendText(event.Content); err != nil {
					slog.Error("sending text to tts", "error", err)
				}
			case types.EventError:
				slog.Error("llm stream", "error", event.Error)
			}
		}
		fmt.Println()

		if err := wsSession.Flush(); err != nil {
			slog.Error("flushing ws session", "error", err)
		}
		wg.Wait()
		wsSession.Close()

		replyText := strings.TrimSpace(reply.String())
		if replyText != "" {
			conversation = append(conversation, message.NewMessage(message.Assistant, []message.ContentPart{
				message.TextContent{Text: replyText},
			}))
		}
	}

	slog.Info("shutting down")
}
