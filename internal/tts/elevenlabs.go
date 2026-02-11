package tts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

const defaultBaseURL = "wss://api.elevenlabs.io/v1"

type SessionConfig struct {
	APIKey       string
	VoiceID      string
	ModelID      string
	OutputFormat string
	Stability    float64
	Similarity   float64
	Speed        float64
}

type AudioChunk struct {
	Data  []byte
	Error error
	Done  bool
}

type Session struct {
	conn   *websocket.Conn
	audio  chan AudioChunk
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

type wsInitMessage struct {
	Text          string          `json:"text"`
	VoiceSettings *wsVoiceSettings `json:"voice_settings,omitempty"`
}

type wsVoiceSettings struct {
	Stability       float64 `json:"stability"`
	SimilarityBoost float64 `json:"similarity_boost"`
	Speed           float64 `json:"speed"`
}

type wsTextMessage struct {
	Text                 string `json:"text"`
	TryTriggerGeneration bool   `json:"try_trigger_generation,omitempty"`
}

type wsAudioMessage struct {
	Audio   string `json:"audio"`
	IsFinal bool   `json:"isFinal"`
}

func NewSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	url := fmt.Sprintf("%s/text-to-speech/%s/stream-input?model_id=%s&output_format=%s",
		defaultBaseURL, cfg.VoiceID, cfg.ModelID, cfg.OutputFormat)

	header := http.Header{}
	header.Set("xi-api-key", cfg.APIKey)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, header)
	if err != nil {
		return nil, fmt.Errorf("dialing elevenlabs ws: %w", err)
	}

	if err := conn.WriteJSON(wsInitMessage{
		Text: " ",
		VoiceSettings: &wsVoiceSettings{
			Stability:       cfg.Stability,
			SimilarityBoost: cfg.Similarity,
			Speed:           cfg.Speed,
		},
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("sending init message: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &Session{
		conn:   conn,
		audio:  make(chan AudioChunk, 32),
		done:   make(chan struct{}),
		cancel: cancel,
	}

	go s.readLoop(ctx)

	return s, nil
}

func (s *Session) readLoop(ctx context.Context) {
	defer close(s.done)
	defer close(s.audio)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			s.audio <- AudioChunk{Error: fmt.Errorf("ws read: %w", err)}
			return
		}

		var am wsAudioMessage
		if err := json.Unmarshal(msg, &am); err != nil {
			s.audio <- AudioChunk{Error: fmt.Errorf("ws unmarshal: %w", err)}
			return
		}

		if am.IsFinal {
			s.audio <- AudioChunk{Done: true}
			return
		}

		if am.Audio == "" {
			continue
		}

		data, err := base64.StdEncoding.DecodeString(am.Audio)
		if err != nil {
			s.audio <- AudioChunk{Error: fmt.Errorf("ws base64 decode: %w", err)}
			return
		}

		if len(data) > 0 {
			s.audio <- AudioChunk{Data: data}
		}
	}
}

func (s *Session) SendText(text string) error {
	return s.conn.WriteJSON(wsTextMessage{
		Text:                 text,
		TryTriggerGeneration: true,
	})
}

func (s *Session) Flush() error {
	return s.conn.WriteJSON(wsTextMessage{Text: ""})
}

func (s *Session) Audio() <-chan AudioChunk {
	return s.audio
}

func (s *Session) Close() error {
	s.once.Do(func() {
		s.cancel()
		s.conn.Close()
	})
	return nil
}

func (s *Session) Wait() {
	<-s.done
}
