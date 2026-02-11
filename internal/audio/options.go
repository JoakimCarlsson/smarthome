package audio

const (
	DefaultSampleRate      = 16000
	DefaultFrameDurationMs = 30
	DefaultVADMode         = 3
	DefaultSilenceFrames   = 35
	DefaultPreBufferFrames = 8
)

type options struct {
	sampleRate      int
	frameDurationMs int
	vadMode         int
	silenceFrames   int
	preBufferFrames int
}

type Option func(*options)

func WithSampleRate(rate int) Option {
	return func(o *options) {
		o.sampleRate = rate
	}
}

func WithFrameDurationMs(ms int) Option {
	return func(o *options) {
		o.frameDurationMs = ms
	}
}

func WithVADMode(mode int) Option {
	return func(o *options) {
		o.vadMode = mode
	}
}

func WithSilenceFrames(n int) Option {
	return func(o *options) {
		o.silenceFrames = n
	}
}

func WithPreBufferFrames(n int) Option {
	return func(o *options) {
		o.preBufferFrames = n
	}
}

func defaultOptions() options {
	return options{
		sampleRate:      DefaultSampleRate,
		frameDurationMs: DefaultFrameDurationMs,
		vadMode:         DefaultVADMode,
		silenceFrames:   DefaultSilenceFrames,
		preBufferFrames: DefaultPreBufferFrames,
	}
}
