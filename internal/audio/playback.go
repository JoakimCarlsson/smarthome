package audio

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/gordonklaus/portaudio"
)

const PlaybackSampleRate = 24000

type Playback struct {
	stream    *portaudio.Stream
	frameBuf  []int16
	frameSize int
	pending   []byte
}

func NewPlayback() (*Playback, error) {
	frameSize := PlaybackSampleRate / 10
	buf := make([]int16, frameSize)

	stream, err := portaudio.OpenDefaultStream(0, 1, float64(PlaybackSampleRate), frameSize, &buf)
	if err != nil {
		return nil, fmt.Errorf("opening playback stream: %w", err)
	}

	if err := stream.Start(); err != nil {
		stream.Close()
		return nil, fmt.Errorf("starting playback stream: %w", err)
	}

	return &Playback{
		stream:    stream,
		frameBuf:  buf,
		frameSize: frameSize,
	}, nil
}

func (p *Playback) Play(data []byte) error {
	p.pending = append(p.pending, data...)
	frameSizeBytes := p.frameSize * 2

	for len(p.pending) >= frameSizeBytes {
		for i := 0; i < p.frameSize; i++ {
			p.frameBuf[i] = int16(binary.LittleEndian.Uint16(p.pending[i*2:]))
		}
		p.pending = p.pending[frameSizeBytes:]

		if err := p.stream.Write(); err != nil {
			if !strings.Contains(err.Error(), "Output underflowed") {
				return fmt.Errorf("writing playback stream: %w", err)
			}
		}
	}

	return nil
}

func (p *Playback) Flush() error {
	if len(p.pending) < 2 {
		p.pending = p.pending[:0]
		return nil
	}

	samples := len(p.pending) / 2
	for i := 0; i < samples; i++ {
		p.frameBuf[i] = int16(binary.LittleEndian.Uint16(p.pending[i*2:]))
	}
	for i := samples; i < p.frameSize; i++ {
		p.frameBuf[i] = 0
	}
	p.pending = p.pending[:0]

	if err := p.stream.Write(); err != nil {
		if !strings.Contains(err.Error(), "Output underflowed") {
			return fmt.Errorf("writing playback stream: %w", err)
		}
	}

	return nil
}

func (p *Playback) Close() error {
	if p.stream != nil {
		p.stream.Stop()
		p.stream.Close()
	}
	return nil
}
