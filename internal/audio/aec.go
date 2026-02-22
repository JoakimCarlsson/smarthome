package audio

/*
#cgo LDFLAGS: -lspeexdsp
#include <speex/speex_echo.h>
#include <stdlib.h>

static SpeexEchoState* aec_init(int frame_size, int filter_length, int sample_rate) {
	SpeexEchoState *st = speex_echo_state_init(frame_size, filter_length);
	speex_echo_ctl(st, SPEEX_ECHO_SET_SAMPLING_RATE, &sample_rate);
	return st;
}

static void aec_process(SpeexEchoState *st, const short *mic, const short *ref, short *out) {
	speex_echo_cancellation(st, mic, ref, out);
}

static void aec_destroy(SpeexEchoState *st) {
	speex_echo_state_destroy(st);
}
*/
import "C"
import (
	"sync"
	"unsafe"
)

type EchoCanceller struct {
	st        *C.SpeexEchoState
	frameSize int
	refBuf    []int16
	mu        sync.Mutex
}

func NewEchoCanceller(frameSize, sampleRate int) *EchoCanceller {
	filterLen := sampleRate * 300 / 1000
	st := C.aec_init(C.int(frameSize), C.int(filterLen), C.int(sampleRate))
	return &EchoCanceller{
		st:        st,
		frameSize: frameSize,
	}
}

func (e *EchoCanceller) FeedReference(samples []int16) {
	e.mu.Lock()
	e.refBuf = append(e.refBuf, samples...)
	e.mu.Unlock()
}

func (e *EchoCanceller) Process(mic []int16) []int16 {
	e.mu.Lock()
	ref := make([]int16, e.frameSize)
	if len(e.refBuf) >= e.frameSize {
		copy(ref, e.refBuf[:e.frameSize])
		e.refBuf = e.refBuf[e.frameSize:]
	}
	e.mu.Unlock()

	out := make([]int16, e.frameSize)
	C.aec_process(
		e.st,
		(*C.short)(unsafe.Pointer(&mic[0])),
		(*C.short)(unsafe.Pointer(&ref[0])),
		(*C.short)(unsafe.Pointer(&out[0])),
	)
	return out
}

func (e *EchoCanceller) Close() {
	if e.st != nil {
		C.aec_destroy(e.st)
		e.st = nil
	}
}

func Resample24to16(in []int16) []int16 {
	outLen := len(in) * 2 / 3
	out := make([]int16, outLen)
	for i := 0; i < outLen; i++ {
		srcPos := float64(i) * 3.0 / 2.0
		idx := int(srcPos)
		frac := srcPos - float64(idx)
		if idx+1 < len(in) {
			out[i] = int16(float64(in[idx])*(1-frac) + float64(in[idx+1])*frac)
		} else if idx < len(in) {
			out[i] = in[idx]
		}
	}
	return out
}
