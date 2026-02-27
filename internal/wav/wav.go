package wav

import (
	"encoding/binary"
	"fmt"
)

// Decode parses a WAV file and returns float32 samples and sample rate.
func Decode(data []byte) ([]float32, int32, error) {
	if len(data) < 44 {
		return nil, 0, fmt.Errorf("file too small for WAV header")
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("not a WAV file")
	}

	offset := 12
	var audioFormat, numChannels, bitsPerSample uint16
	var sampleRate uint32
	foundFmt := false

	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		chunkSize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		if chunkID == "fmt " {
			if chunkSize < 16 {
				return nil, 0, fmt.Errorf("fmt chunk too small")
			}
			audioFormat = binary.LittleEndian.Uint16(data[offset+8:])
			numChannels = binary.LittleEndian.Uint16(data[offset+10:])
			sampleRate = binary.LittleEndian.Uint32(data[offset+12:])
			bitsPerSample = binary.LittleEndian.Uint16(data[offset+22:])
			foundFmt = true
			offset += 8 + int(chunkSize)
			continue
		}
		if chunkID == "data" && foundFmt {
			if audioFormat != 1 {
				return nil, 0, fmt.Errorf("only PCM WAV supported (got format %d)", audioFormat)
			}
			end := offset + 8 + int(chunkSize)
			if end > len(data) {
				end = len(data)
			}
			pcmData := data[offset+8 : end]
			samples := pcmToFloat32(pcmData, bitsPerSample, numChannels)
			return samples, int32(sampleRate), nil
		}
		offset += 8 + int(chunkSize)
	}
	return nil, 0, fmt.Errorf("missing fmt or data chunk")
}

// Encode creates a 16-bit mono PCM WAV from float32 samples.
func Encode(samples []float32, sampleRate int) []byte {
	numSamples := len(samples)
	dataSize := numSamples * 2
	fileSize := 36 + dataSize
	buf := make([]byte, 0, fileSize+8)

	// RIFF header
	buf = append(buf, "RIFF"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(fileSize))
	buf = append(buf, "WAVE"...)

	// fmt chunk
	buf = append(buf, "fmt "...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1)                    // PCM
	buf = binary.LittleEndian.AppendUint16(buf, 1)                    // mono
	buf = binary.LittleEndian.AppendUint32(buf, uint32(sampleRate))   // sample rate
	buf = binary.LittleEndian.AppendUint32(buf, uint32(sampleRate*2)) // byte rate
	buf = binary.LittleEndian.AppendUint16(buf, 2)                    // block align
	buf = binary.LittleEndian.AppendUint16(buf, 16)                   // bits per sample

	// data chunk
	buf = append(buf, "data"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(dataSize))
	for _, s := range samples {
		if s > 1.0 {
			s = 1.0
		} else if s < -1.0 {
			s = -1.0
		}
		buf = binary.LittleEndian.AppendUint16(buf, uint16(int16(s*32767)))
	}

	return buf
}

func pcmToFloat32(data []byte, bitsPerSample, numChannels uint16) []float32 {
	bytesPerSample := int(bitsPerSample / 8)
	frameSize := int(numChannels) * bytesPerSample
	numFrames := len(data) / frameSize
	samples := make([]float32, numFrames)

	for i := 0; i < numFrames; i++ {
		off := i * frameSize
		switch bitsPerSample {
		case 16:
			s := int16(binary.LittleEndian.Uint16(data[off:]))
			samples[i] = float32(s) / 32768.0
		case 32:
			s := int32(binary.LittleEndian.Uint32(data[off:]))
			samples[i] = float32(s) / 2147483648.0
		}
	}
	return samples
}
