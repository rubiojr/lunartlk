package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/kazzmir/opus-go/opus"
)

const (
	// Opus frame size: 20ms at 16kHz = 320 samples
	FrameSize  = 320
	SampleRate = 16000
	channels   = 1
	// Max encoded frame size
	maxFrameBytes = 1024
)

// StreamEncoder encodes PCM audio to Opus incrementally.
type StreamEncoder struct {
	enc    *opus.Encoder
	buf    []float32
	out    bytes.Buffer
	frames [][]byte // individual encoded frames for Ogg muxing
	frame  []byte
	mu     sync.Mutex
}

// NewStreamEncoder creates a streaming Opus encoder.
func NewStreamEncoder(bitrate int) (*StreamEncoder, error) {
	enc, err := opus.NewEncoder(SampleRate, channels, opus.ApplicationVoIP)
	if err != nil {
		return nil, fmt.Errorf("create encoder: %w", err)
	}
	if err := enc.SetBitrate(bitrate); err != nil {
		return nil, fmt.Errorf("set bitrate: %w", err)
	}
	return &StreamEncoder{
		enc:   enc,
		frame: make([]byte, maxFrameBytes),
	}, nil
}

// float32ToInt16 converts float32 PCM samples [-1.0, 1.0] to int16.
func float32ToInt16(in []float32, out []int16) {
	for i, s := range in {
		if s > 1.0 {
			s = 1.0
		} else if s < -1.0 {
			s = -1.0
		}
		out[i] = int16(s * 32767)
	}
}

// Write adds PCM samples and encodes any complete frames.
func (s *StreamEncoder) Write(samples []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.buf = append(s.buf, samples...)

	pcm16 := make([]int16, FrameSize)
	for len(s.buf) >= FrameSize {
		pcm := s.buf[:FrameSize]
		s.buf = s.buf[FrameSize:]

		float32ToInt16(pcm, pcm16)
		n, err := s.enc.Encode(pcm16, FrameSize, s.frame)
		if err != nil {
			return fmt.Errorf("encode frame: %w", err)
		}
		// Save for wire format
		binary.Write(&s.out, binary.LittleEndian, uint16(n))
		s.out.Write(s.frame[:n])
		// Save individual frame for Ogg muxing
		frameCopy := make([]byte, n)
		copy(frameCopy, s.frame[:n])
		s.frames = append(s.frames, frameCopy)
	}
	return nil
}

// Flush encodes any remaining samples (padded with silence).
func (s *StreamEncoder) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.buf) == 0 {
		return nil
	}
	pcm := make([]float32, FrameSize)
	copy(pcm, s.buf)
	s.buf = nil

	pcm16 := make([]int16, FrameSize)
	float32ToInt16(pcm, pcm16)
	n, err := s.enc.Encode(pcm16, FrameSize, s.frame)
	if err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	binary.Write(&s.out, binary.LittleEndian, uint16(n))
	s.out.Write(s.frame[:n])
	frameCopy := make([]byte, n)
	copy(frameCopy, s.frame[:n])
	s.frames = append(s.frames, frameCopy)
	return nil
}

// Bytes returns the encoded Opus data in wire format (for server transfer).
func (s *StreamEncoder) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Bytes()
}

// OggBytes returns the encoded audio as a standard Ogg Opus file (playable by media players).
func (s *StreamEncoder) OggBytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return OggOpus(s.frames, SampleRate, channels)
}

// EncodeOpus encodes float32 PCM samples to Opus in one shot.
func EncodeOpus(samples []float32, bitrate int) ([]byte, error) {
	se, err := NewStreamEncoder(bitrate)
	if err != nil {
		return nil, err
	}
	if err := se.Write(samples); err != nil {
		return nil, err
	}
	if err := se.Flush(); err != nil {
		return nil, err
	}
	return se.Bytes(), nil
}

// DecodeOpus decodes an Opus stream back to float32 PCM samples.
func DecodeOpus(data []byte) ([]float32, int32, error) {
	dec, err := opus.NewDecoder(SampleRate, channels)
	if err != nil {
		return nil, 0, fmt.Errorf("create decoder: %w", err)
	}
	defer dec.Close()

	r := bytes.NewReader(data)
	var samples []float32
	pcm := make([]float32, FrameSize)

	for {
		var frameLen uint16
		if err := binary.Read(r, binary.LittleEndian, &frameLen); err != nil {
			if err == io.EOF {
				break
			}
			return nil, 0, fmt.Errorf("read frame length: %w", err)
		}

		frame := make([]byte, frameLen)
		if _, err := io.ReadFull(r, frame); err != nil {
			return nil, 0, fmt.Errorf("read frame data: %w", err)
		}

		n, err := dec.DecodeF32(frame, pcm, FrameSize, false)
		if err != nil {
			return nil, 0, fmt.Errorf("decode frame: %w", err)
		}

		samples = append(samples, pcm[:n]...)
	}

	return samples, SampleRate, nil
}
