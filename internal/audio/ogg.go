package audio

import (
	"bytes"
	"encoding/binary"
)

// OggOpus wraps Opus frames in a standard Ogg Opus container.
// The result is playable by any media player.
func OggOpus(opusFrames [][]byte, sampleRate, channels int) []byte {
	var buf bytes.Buffer
	serial := uint32(0x4C554E41) // "LUNA"

	// Page 1: OpusHead
	head := makeOpusHead(sampleRate, channels)
	writeOggPage(&buf, serial, 0, 0, 2, [][]byte{head}) // granule=0, BOS flag

	// Page 2: OpusTags
	tags := makeOpusTags()
	writeOggPage(&buf, serial, 0, 1, 0, [][]byte{tags})

	// Audio pages: pack multiple frames per page (up to ~50ms worth)
	var pageFrames [][]byte
	var granulePos uint64
	pageSeq := uint32(2)
	samplesPerFrame := uint64(FrameSize) // 320 samples = 20ms at 16kHz

	for i, frame := range opusFrames {
		pageFrames = append(pageFrames, frame)
		granulePos += samplesPerFrame

		// Flush page every ~200ms (10 frames) or at end
		if len(pageFrames) >= 10 || i == len(opusFrames)-1 {
			flags := byte(0)
			if i == len(opusFrames)-1 {
				flags = 4 // EOS
			}
			writeOggPage(&buf, serial, granulePos, pageSeq, flags, pageFrames)
			pageSeq++
			pageFrames = nil
		}
	}

	return buf.Bytes()
}

func makeOpusHead(sampleRate, channels int) []byte {
	var buf bytes.Buffer
	buf.WriteString("OpusHead")
	buf.WriteByte(1) // version
	buf.WriteByte(byte(channels))
	binary.Write(&buf, binary.LittleEndian, uint16(0))          // pre-skip
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate)) // input sample rate
	binary.Write(&buf, binary.LittleEndian, int16(0))           // output gain
	buf.WriteByte(0)                                            // channel mapping family
	return buf.Bytes()
}

func makeOpusTags() []byte {
	var buf bytes.Buffer
	buf.WriteString("OpusTags")
	vendor := "lunartlk"
	binary.Write(&buf, binary.LittleEndian, uint32(len(vendor)))
	buf.WriteString(vendor)
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // no user comments
	return buf.Bytes()
}

// writeOggPage writes a single Ogg page.
// flags: 0=none, 2=BOS (beginning of stream), 4=EOS (end of stream)
func writeOggPage(buf *bytes.Buffer, serial uint32, granule uint64, seqNo uint32, flags byte, segments [][]byte) {
	// Build segment table
	var segTable []byte
	for _, seg := range segments {
		n := len(seg)
		for n >= 255 {
			segTable = append(segTable, 255)
			n -= 255
		}
		segTable = append(segTable, byte(n))
	}

	// Header (27 bytes + segment table)
	var hdr bytes.Buffer
	hdr.WriteString("OggS")                            // capture pattern
	hdr.WriteByte(0)                                   // version
	hdr.WriteByte(flags)                               // header type
	binary.Write(&hdr, binary.LittleEndian, granule)   // granule position
	binary.Write(&hdr, binary.LittleEndian, serial)    // serial number
	binary.Write(&hdr, binary.LittleEndian, seqNo)     // page sequence
	binary.Write(&hdr, binary.LittleEndian, uint32(0)) // CRC placeholder
	hdr.WriteByte(byte(len(segTable)))                 // number of segments
	hdr.Write(segTable)

	// Collect page data
	var pageData bytes.Buffer
	for _, seg := range segments {
		pageData.Write(seg)
	}

	// Compute CRC32
	page := append(hdr.Bytes(), pageData.Bytes()...)
	crc := crc32Ogg(page)
	page[22] = byte(crc)
	page[23] = byte(crc >> 8)
	page[24] = byte(crc >> 16)
	page[25] = byte(crc >> 24)

	buf.Write(page)
}

// Ogg uses a custom CRC32 with polynomial 0x04C11DB7 (no bit reversal)
var oggCRCTable [256]uint32

func init() {
	for i := 0; i < 256; i++ {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04C11DB7
			} else {
				r <<= 1
			}
		}
		oggCRCTable[i] = r
	}
}

func crc32Ogg(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		crc = (crc << 8) ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}
