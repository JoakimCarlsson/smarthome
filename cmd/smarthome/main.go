package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"fmt"
	"strings"

	aiaudio "github.com/joakimcarlsson/ai/audio"
	"github.com/joakimcarlsson/ai/message"
	"github.com/joakimcarlsson/ai/model"
	llm "github.com/joakimcarlsson/ai/providers"
	"github.com/joakimcarlsson/ai/transcription"
	"github.com/joakimcarlsson/ai/types"
	"github.com/joakimcarlsson/smarthome/internal/audio"
	"github.com/joakimcarlsson/smarthome/internal/config"
	"github.com/joakimcarlsson/smarthome/internal/otel"
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

	ttsClient, err := aiaudio.NewAudioGeneration(
		model.ProviderElevenLabs,
		aiaudio.WithAPIKey(cfg.ElevenLabsAPIKey),
		aiaudio.WithModel(model.ElevenLabsAudioModels[model.ElevenTurboV2_5]),
	)
	if err != nil {
		slog.Error("creating tts client", "error", err)
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

		var reply strings.Builder
		stream := llmClient.StreamResponse(ctx, conversation, nil)

		for event := range stream {
			switch event.Type {
			case types.EventContentDelta:
				fmt.Print(event.Content)
				reply.WriteString(event.Content)
			case types.EventError:
				slog.Error("llm stream", "error", event.Error)
			}
		}
		fmt.Println()

		replyText := strings.TrimSpace(reply.String())
		if replyText == "" {
			continue
		}

		conversation = append(conversation, message.NewMessage(message.Assistant, []message.ContentPart{
			message.TextContent{Text: replyText},
		}))

		speakText(ctx, ttsClient, speaker, cfg.ElevenLabsVoiceID, replyText)
	}

	slog.Info("shutting down")
}

func speakText(ctx context.Context, tts aiaudio.AudioGeneration, speaker *audio.Playback, voiceID, text string) {
	if text == "" {
		return
	}

	chunks, err := tts.StreamAudio(ctx, text,
		aiaudio.WithVoiceID(voiceID),
		aiaudio.WithOutputFormat("pcm_24000"),
	)
	if err != nil {
		slog.Error("streaming tts", "error", err)
		return
	}

	var buf []byte
	buffered := false

	for chunk := range chunks {
		if chunk.Error != nil {
			slog.Error("tts chunk", "error", chunk.Error)
			return
		}
		if chunk.Done {
			break
		}

		buf = append(buf, chunk.Data...)

		if !buffered && len(buf) < 16384 {
			continue
		}
		buffered = true

		if err := speaker.Play(buf); err != nil {
			slog.Error("playing audio", "error", err)
			return
		}
		buf = buf[:0]
	}

	if len(buf) > 0 {
		if err := speaker.Play(buf); err != nil {
			slog.Error("playing audio", "error", err)
		}
	}
	if err := speaker.Flush(); err != nil {
		slog.Error("flushing audio", "error", err)
	}
}
