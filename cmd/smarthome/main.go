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
	serviceName       = "smarthome"
	serviceVersion    = "0.1.0"
	noSpeechThreshold = 0.6
)

//go:embed prompts/system.md
var systemPrompt string

//go:embed res/show-bror_en_linux_v4_0_0.ppn
var wakeWordModel []byte

func main() {
	cfg, err := config.Load("../../.env")
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

	wakeWordFile, err := os.CreateTemp("", "wakeword-*.ppn")
	if err != nil {
		slog.Error("creating wake word temp file", "error", err)
		os.Exit(1)
	}
	if _, err := wakeWordFile.Write(wakeWordModel); err != nil {
		slog.Error("writing wake word temp file", "error", err)
		os.Exit(1)
	}
	wakeWordFile.Close()
	defer os.Remove(wakeWordFile.Name())

	frameSize := audio.DefaultSampleRate * audio.DefaultFrameDurationMs / 1000
	aec := audio.NewEchoCanceller(frameSize, audio.DefaultSampleRate)
	defer aec.Close()

	mic, err := audio.New(aec,
		audio.WithWakeWord(cfg.PicovoiceAccessKey, wakeWordFile.Name()),
	)
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
		transcription.WithAPIKey(cfg.OpenAIAPIKey),
		transcription.WithModel(model.OpenAITranscriptionModels[model.Whisper1]),
	)
	if err != nil {
		slog.Error("creating stt client", "error", err)
		os.Exit(1)
	}

	llmClient, err := llm.NewLLM(
		model.ProviderAnthropic,
		llm.WithAPIKey(cfg.AnthropicAPIKey),
		llm.WithModel(model.AnthropicModels[model.Claude45Haiku]),
	)
	if err != nil {
		slog.Error("creating llm client", "error", err)
		os.Exit(1)
	}

	myAgent := agent.New(llmClient,
		agent.WithSystemPrompt(systemPrompt),
		agent.WithTools(tools.NewWebSearchTool(cfg.SerpAPIKey)),
	)

	speaker, err := audio.NewPlayback(aec)
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
		"stt", "openai/whisper-1",
		"llm", "anthropic/claude-4.5-haiku",
	)

	var cancelCurrent context.CancelFunc
	var currentDone chan struct{}
	processing := false

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-currentDone:
				processing = false
			}
		}
	}()

	for pcm := range utterances {
		if processing {
			wav := audio.EncodeWAV(pcm, audio.DefaultSampleRate, 1, 16)
			resp, err := stt.Transcribe(ctx, wav,
				transcription.WithLanguage("sv"),
				transcription.WithFilename("audio.wav"),
			)
			if err != nil {
				slog.Debug("barge-in STT failed, ignoring", "error", err)
				continue
			}
			text := strings.TrimSpace(resp.Text)
			if text == "" || isHallucination(resp) {
				slog.Debug("discarding non-speech interrupt")
				continue
			}
			slog.Info("barge-in confirmed", "text", text)
			cancelCurrent()
			<-currentDone
			speaker.Reset()

			utterCtx, utterCancel := context.WithCancel(ctx)
			cancelCurrent = utterCancel
			currentDone = make(chan struct{})
			processing = true

			go processUtterance(utterCtx, currentDone, text, stt, myAgent, speaker, ttsConfig)
			continue
		}

		utterCtx, utterCancel := context.WithCancel(ctx)
		cancelCurrent = utterCancel
		currentDone = make(chan struct{})
		processing = true

		go processUtterance(utterCtx, currentDone, "", stt, myAgent, speaker, ttsConfig, pcm)
	}

	if cancelCurrent != nil {
		cancelCurrent()
		<-currentDone
	}

	slog.Info("shutting down")
}

func processUtterance(
	ctx context.Context,
	done chan struct{},
	preTranscribed string,
	stt transcription.SpeechToText,
	myAgent *agent.Agent,
	speaker *audio.Playback,
	ttsConfig tts.SessionConfig,
	pcm ...[]byte,
) {
	defer close(done)

	text := preTranscribed

	var wsSession *tts.Session
	var wsErr error
	wsDone := make(chan struct{})
	go func() {
		wsSession, wsErr = tts.NewSession(ctx, ttsConfig)
		close(wsDone)
	}()

	if text == "" && len(pcm) > 0 {
		wav := audio.EncodeWAV(pcm[0], audio.DefaultSampleRate, 1, 16)

		resp, err := stt.Transcribe(ctx, wav,
			transcription.WithLanguage("sv"),
			transcription.WithFilename("audio.wav"),
		)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("interrupted during transcription")
			} else {
				slog.Error("transcribing", "error", err)
			}
			<-wsDone
			if wsSession != nil {
				wsSession.Close()
			}
			return
		}

		text = strings.TrimSpace(resp.Text)
		if text == "" || isHallucination(resp) {
			if text != "" {
				slog.Debug("discarding hallucination", "text", text)
			}
			<-wsDone
			if wsSession != nil {
				wsSession.Close()
			}
			return
		}

		slog.Info("transcribed", "text", text)
	}

	<-wsDone
	if wsErr != nil {
		if ctx.Err() != nil {
			slog.Info("interrupted during tts connect")
		} else {
			slog.Error("creating ws session", "error", wsErr)
		}
		return
	}
	defer wsSession.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for chunk := range wsSession.Audio() {
			if ctx.Err() != nil {
				return
			}
			if chunk.Error != nil {
				if ctx.Err() == nil {
					slog.Error("tts chunk", "error", chunk.Error)
				}
				return
			}
			if chunk.Done {
				break
			}
			if err := speaker.Play(chunk.Data); err != nil {
				if ctx.Err() == nil {
					slog.Error("playing audio", "error", err)
				}
				return
			}
		}
		if ctx.Err() == nil {
			if err := speaker.Flush(); err != nil {
				slog.Error("flushing audio", "error", err)
			}
		}
	}()

	for event := range myAgent.ChatStream(ctx, text) {
		if ctx.Err() != nil {
			break
		}
		switch event.Type {
		case types.EventContentDelta:
			fmt.Print(event.Content)
			if err := wsSession.SendText(event.Content); err != nil {
				if ctx.Err() == nil {
					slog.Error("sending text to tts", "error", err)
				}
			}
		case types.EventError:
			if ctx.Err() == nil {
				slog.Error("agent stream", "error", event.Error)
			}
		}
	}
	fmt.Println()

	if ctx.Err() == nil {
		if err := wsSession.Flush(); err != nil {
			slog.Error("flushing ws session", "error", err)
		}
	}

	wg.Wait()

	if ctx.Err() != nil {
		slog.Info("interrupted")
	}
}

func isHallucination(resp *transcription.TranscriptionResponse) bool {
	if len(resp.Segments) == 0 {
		return false
	}
	for _, seg := range resp.Segments {
		if seg.NoSpeechProb >= noSpeechThreshold {
			return true
		}
	}
	return false
}
