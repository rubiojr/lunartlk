package client

// #cgo pkg-config: portaudio-2.0 jack
import "C"

import (
	"fmt"
	"sync"

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
func (r *Recorder) Stop() []float32 {
	close(r.done)
	<-r.stopped // Read() returns within ~64ms, goroutine sees done and exits
	r.stream.Stop()
	r.stream.Close()

	r.mu.Lock()
	samples := r.recorded
	r.recorded = nil
	r.mu.Unlock()

	return samples
}

// Close terminates PortAudio. Must be called after Stop.
func (r *Recorder) Close() error {
	return portaudio.Terminate()
}
