package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"

	"github.com/gordonklaus/portaudio"
	webrtcvad "github.com/maxhawkins/go-webrtcvad"
)

type Capture struct {
	opts   options
	vad    *webrtcvad.VAD
	stream *portaudio.Stream
}

func New(opts ...Option) (*Capture, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}

	vad, err := webrtcvad.New()
	if err != nil {
		return nil, fmt.Errorf("creating vad: %w", err)
	}

	if err := vad.SetMode(o.vadMode); err != nil {
		return nil, fmt.Errorf("setting vad mode: %w", err)
	}

	frameSize := o.sampleRate * o.frameDurationMs / 1000

	if !vad.ValidRateAndFrameLength(o.sampleRate, frameSize) {
		return nil, fmt.Errorf("invalid sample rate %d or frame size %d for vad", o.sampleRate, frameSize)
	}

	return &Capture{
		opts: o,
		vad:  vad,
	}, nil
}

func (c *Capture) Start(ctx context.Context) (<-chan []byte, error) {
	if err := portaudio.Initialize(); err != nil {
		return nil, fmt.Errorf("initializing portaudio: %w", err)
	}

	frameSize := c.opts.sampleRate * c.opts.frameDurationMs / 1000
	buf := make([]int16, frameSize)

	stream, err := portaudio.OpenDefaultStream(1, 0, float64(c.opts.sampleRate), frameSize, buf)
	if err != nil {
		portaudio.Terminate()
		return nil, fmt.Errorf("opening stream: %w", err)
	}
	c.stream = stream

	if err := stream.Start(); err != nil {
		stream.Close()
		portaudio.Terminate()
		return nil, fmt.Errorf("starting stream: %w", err)
	}

	ch := make(chan []byte, 4)
	go c.captureLoop(ctx, buf, ch)
	return ch, nil
}

func (c *Capture) Close() error {
	if c.stream != nil {
		c.stream.Stop()
		c.stream.Close()
	}
	return portaudio.Terminate()
}

func (c *Capture) captureLoop(ctx context.Context, buf []int16, ch chan<- []byte) {
	defer close(ch)

	ring := newRingBuffer(c.opts.preBufferFrames)
	var utterance []byte
	silenceCount := 0
	speaking := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := c.stream.Read(); err != nil {
			slog.Error("reading audio stream", "error", err)
			continue
		}

		frame := samplesToBytes(buf)

		active, err := c.vad.Process(c.opts.sampleRate, frame)
		if err != nil {
			slog.Error("processing vad", "error", err)
			continue
		}

		if active {
			if !speaking {
				speaking = true
				silenceCount = 0
				utterance = ring.Drain()
			}
			utterance = append(utterance, frame...)
		} else {
			ring.Push(frame)
			if speaking {
				utterance = append(utterance, frame...)
				silenceCount++
				if silenceCount >= c.opts.silenceFrames {
					select {
					case ch <- utterance:
					case <-ctx.Done():
						return
					}
					utterance = nil
					speaking = false
					silenceCount = 0
				}
			}
		}
	}
}

func samplesToBytes(samples []int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(s))
	}
	return b
}

type ringBuffer struct {
	buf  [][]byte
	size int
	pos  int
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		buf:  make([][]byte, size),
		size: size,
	}
}

func (r *ringBuffer) Push(frame []byte) {
	cp := make([]byte, len(frame))
	copy(cp, frame)
	r.buf[r.pos] = cp
	r.pos = (r.pos + 1) % r.size
	if r.pos == 0 {
		r.full = true
	}
}

func (r *ringBuffer) Drain() []byte {
	var out []byte
	if r.full {
		for i := r.pos; i < r.size; i++ {
			if r.buf[i] != nil {
				out = append(out, r.buf[i]...)
			}
		}
	}
	for i := 0; i < r.pos; i++ {
		if r.buf[i] != nil {
			out = append(out, r.buf[i]...)
		}
	}
	r.pos = 0
	r.full = false
	return out
}
