package client

// #cgo pkg-config: portaudio-2.0 jack
import "C"

import (
	"fmt"
	"sync"
	"time"

	"github.com/gordonklaus/portaudio"
)

// Recorder captures audio from the default input device via PortAudio.
type Recorder struct {
	sampleRate int
	chunkSize  int
	stream     *portaudio.Stream
	buf        []float32
	recorded   []float32
	mu         sync.Mutex
	done       chan struct{}
	stopped    chan struct{}
}

// Segment is a chunk of recorded audio delivered by StartContinuous.
type Segment struct {
	Samples []float32
}

// NewRecorder initializes PortAudio and opens the default input stream.
// Call Close when finished to release PortAudio resources.
func NewRecorder(sampleRate, chunkSize int) (*Recorder, error) {
	if err := portaudio.Initialize(); err != nil {
		return nil, fmt.Errorf("portaudio init: %w", err)
	}

	buf := make([]float32, chunkSize)
	stream, err := portaudio.OpenDefaultStream(1, 0, float64(sampleRate), chunkSize, buf)
	if err != nil {
		portaudio.Terminate()
		return nil, fmt.Errorf("open mic: %w", err)
	}

	return &Recorder{
		sampleRate: sampleRate,
		chunkSize:  chunkSize,
		stream:     stream,
		buf:        buf,
		done:       make(chan struct{}),
		stopped:    make(chan struct{}),
	}, nil
}

// Start begins capturing audio in a background goroutine.
func (r *Recorder) Start() error {
	if err := r.stream.Start(); err != nil {
		return fmt.Errorf("start mic: %w", err)
	}
	go r.capture()
	return nil
}

func (r *Recorder) capture() {
	defer close(r.stopped)
	for {
		select {
		case <-r.done:
			return
		default:
		}

		if err := r.stream.Read(); err != nil {
			return
		}
		chunk := make([]float32, r.chunkSize)
		copy(chunk, r.buf)
		r.mu.Lock()
		r.recorded = append(r.recorded, chunk...)
		r.mu.Unlock()
	}
}

// Stop ends the recording and returns the captured samples.
// The recorder can be restarted by calling Start again.
func (r *Recorder) Stop() []float32 {
	close(r.done)
	<-r.stopped
	r.stream.Stop()

	r.mu.Lock()
	samples := r.recorded
	r.recorded = nil
	r.mu.Unlock()

	// Reset channels for reuse
	r.done = make(chan struct{})
	r.stopped = make(chan struct{})

	return samples
}

// Close releases the PortAudio stream and terminates PortAudio.
func (r *Recorder) Close() error {
	r.stream.Close()
	return portaudio.Terminate()
}

// StartContinuous begins recording and delivers audio segments of the given
// duration to the returned channel. Recording continues until StopContinuous
// is called. The stream stays open between segments (no gaps).
func (r *Recorder) StartContinuous(segmentDuration time.Duration) (<-chan Segment, error) {
	if err := r.stream.Start(); err != nil {
		return nil, fmt.Errorf("start mic: %w", err)
	}

	r.done = make(chan struct{})
	r.stopped = make(chan struct{})
	ch := make(chan Segment, 2)
	samplesPerSegment := int(segmentDuration.Seconds()) * r.sampleRate

	go func() {
		defer close(r.stopped)
		defer close(ch)

		var segment []float32
		for {
			select {
			case <-r.done:
				// Deliver any remaining audio
				if len(segment) > 0 {
					ch <- Segment{Samples: segment}
				}
				return
			default:
			}

			if err := r.stream.Read(); err != nil {
				return
			}
			chunk := make([]float32, r.chunkSize)
			copy(chunk, r.buf)
			segment = append(segment, chunk...)

			if len(segment) >= samplesPerSegment {
				ch <- Segment{Samples: segment}
				segment = nil
			}
		}
	}()

	return ch, nil
}

// StopContinuous stops a continuous recording session.
func (r *Recorder) StopContinuous() {
	close(r.done)
	<-r.stopped
	r.stream.Stop()
}
