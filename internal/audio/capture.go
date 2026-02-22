package audio

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/gordonklaus/portaudio"
	webrtcvad "github.com/maxhawkins/go-webrtcvad"
)

const wakeWordScript = `import sys
import struct
import traceback

try:
    import pvporcupine
    porcupine = pvporcupine.create(
        access_key=sys.argv[1],
        keyword_paths=[sys.argv[2]],
        sensitivities=[0.9]
    )
except Exception:
    traceback.print_exc()
    sys.exit(1)

print("READY", flush=True)
frame_length = porcupine.frame_length
frames_processed = 0

try:
    while True:
        data = sys.stdin.buffer.read(frame_length * 2)
        if len(data) < frame_length * 2:
            print(f"EOF after {frames_processed} frames, got {len(data)} bytes", file=sys.stderr, flush=True)
            break
        frame = struct.unpack_from(f"{frame_length}h", data)
        result = porcupine.process(frame)
        frames_processed += 1
        if result >= 0:
            names = [sys.argv[2], "porcupine"]
            print(f"WAKE", flush=True)
            print(f"detected keyword index {result}: {names[result]}", file=sys.stderr, flush=True)
            break
except BaseException as e:
    print(f"caught {type(e).__name__}: {e}", file=sys.stderr, flush=True)
    traceback.print_exc()

porcupine.delete()
print(f"exiting after {frames_processed} frames", file=sys.stderr, flush=True)
`

type wakeWordProc struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Scanner
	scriptPath string
}

type Capture struct {
	opts   options
	vad    *webrtcvad.VAD
	stream *portaudio.Stream
	aec    *EchoCanceller
}

func New(aec *EchoCanceller, opts ...Option) (*Capture, error) {
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

	if o.wakeWordAccessKey != "" && o.wakeWordModelPath != "" {
		slog.Info("wake word enabled", "model", o.wakeWordModelPath)
	}

	return &Capture{
		opts: o,
		vad:  vad,
		aec:  aec,
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

func (c *Capture) startWakeWordProcess() (*wakeWordProc, error) {
	scriptFile, err := os.CreateTemp("", "wakeword-*.py")
	if err != nil {
		return nil, fmt.Errorf("creating script temp file: %w", err)
	}
	if _, err := scriptFile.WriteString(wakeWordScript); err != nil {
		os.Remove(scriptFile.Name())
		return nil, fmt.Errorf("writing script temp file: %w", err)
	}
	scriptFile.Close()

	cmd := exec.Command("python3", scriptFile.Name(), c.opts.wakeWordAccessKey, c.opts.wakeWordModelPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("creating stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		os.Remove(scriptFile.Name())
		return nil, fmt.Errorf("starting wake word process: %w", err)
	}
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			slog.Error("wake word process", "stderr", scanner.Text())
		}
	}()

	scanner := bufio.NewScanner(stdoutPipe)
	readyCh := make(chan bool, 1)
	go func() {
		readyCh <- scanner.Scan() && scanner.Text() == "READY"
	}()

	select {
	case ready := <-readyCh:
		if !ready {
			stdin.Close()
			exitErr := cmd.Wait()
			os.Remove(scriptFile.Name())
			return nil, fmt.Errorf("wake word process failed to start: %v", exitErr)
		}
	case <-time.After(10 * time.Second):
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
		os.Remove(scriptFile.Name())
		return nil, fmt.Errorf("wake word process timed out waiting for READY")
	}
	slog.Info("wake word process ready", "pid", cmd.Process.Pid)

	return &wakeWordProc{
		cmd:        cmd,
		stdin:      stdin,
		stdout:     scanner,
		scriptPath: scriptFile.Name(),
	}, nil
}

func (w *wakeWordProc) kill() {
	if w == nil {
		return
	}
	w.stdin.Close()
	w.cmd.Process.Kill()
	w.cmd.Wait()
	os.Remove(w.scriptPath)
}

func (c *Capture) captureLoop(ctx context.Context, buf []int16, ch chan<- []byte) {
	defer close(ch)

	useWakeWord := c.opts.wakeWordAccessKey != "" && c.opts.wakeWordModelPath != ""

	ring := newRingBuffer(c.opts.preBufferFrames)
	var utterance []byte
	silenceCount := 0
	activeCount := 0
	speaking := false
	awake := !useWakeWord
	var awakeExpiry time.Time

	var ww *wakeWordProc
	var wwDetected chan bool
	if useWakeWord {
		var err error
		ww, err = c.startWakeWordProcess()
		if err != nil {
			slog.Error("starting wake word process", "error", err)
			return
		}
		wwDetected = make(chan bool, 1)
		go func() {
			if ww.stdout.Scan() && ww.stdout.Text() == "WAKE" {
				wwDetected <- true
			} else {
				wwDetected <- false
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			if ww != nil {
				ww.kill()
			}
			return
		default:
		}

		if err := c.stream.Read(); err != nil {
			slog.Error("reading audio stream", "error", err)
			continue
		}

		if awake && !speaking && useWakeWord && !awakeExpiry.IsZero() && time.Now().After(awakeExpiry) {
			awakeExpiry = time.Time{}
			awake = false
			var err error
			ww, err = c.startWakeWordProcess()
			if err != nil {
				slog.Error("restarting wake word process", "error", err)
				return
			}
			wwDetected = make(chan bool, 1)
			go func() {
				if ww.stdout.Scan() && ww.stdout.Text() == "WAKE" {
					wwDetected <- true
				} else {
					wwDetected <- false
				}
			}()
			continue
		}

		if !awake {
			pcm := samplesToBytes(buf)
			if _, err := ww.stdin.Write(pcm); err != nil {
				select {
				case detected := <-wwDetected:
					if detected {
						slog.Info("wake word detected")
						awake = true
						ww.kill()
						ww = nil
						continue
					}
				default:
				}
				ww.kill()
				var err2 error
				ww, err2 = c.startWakeWordProcess()
				if err2 != nil {
					slog.Error("restarting wake word process", "error", err2)
					return
				}
				wwDetected = make(chan bool, 1)
				go func() {
					if ww.stdout.Scan() && ww.stdout.Text() == "WAKE" {
						wwDetected <- true
					} else {
						wwDetected <- false
					}
				}()
				continue
			}

			select {
			case detected := <-wwDetected:
				if detected {
					slog.Info("wake word detected")
					awake = true
					ww.kill()
					ww = nil
				}
			default:
			}
			continue
		}

		samples := buf
		if c.aec != nil {
			samples = c.aec.Process(buf)
		}

		frame := samplesToBytes(samples)

		active, err := c.vad.Process(c.opts.sampleRate, frame)
		if err != nil {
			slog.Error("processing vad", "error", err)
			continue
		}

		if active {
			if !speaking {
				activeCount++
				ring.Push(frame)
				if activeCount >= c.opts.minActiveFrames {
					slog.Info("speech started")
					speaking = true
					silenceCount = 0
					utterance = ring.Drain()
				}
			} else {
				utterance = append(utterance, frame...)
			}
		} else {
			if !speaking {
				activeCount = 0
			}
			ring.Push(frame)
			if speaking {
				utterance = append(utterance, frame...)
				silenceCount++
				if silenceCount >= c.opts.silenceFrames {
					slog.Info("speech ended")
					select {
					case ch <- utterance:
					case <-ctx.Done():
						if ww != nil {
							ww.kill()
						}
						return
					}
					utterance = nil
					speaking = false
					silenceCount = 0
					activeCount = 0
					if useWakeWord {
						awakeExpiry = time.Now().Add(c.opts.postUtteranceTimeout)
					}
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
