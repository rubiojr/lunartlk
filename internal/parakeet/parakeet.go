package parakeet

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strings"

	ort "github.com/yalue/onnxruntime_go"
)

// Model holds the loaded Parakeet v3 ONNX sessions and vocabulary.
type Model struct {
	preprocessor *ort.DynamicAdvancedSession
	encoder      *ort.DynamicAdvancedSession
	decoder      *ort.DynamicAdvancedSession
	joiner       *ort.DynamicAdvancedSession
	vocab        []string
	blankIdx     int
}

// LoadModel loads the Parakeet v3 model in sherpa-onnx format.
func LoadModel(dir string, ortLibPath string) (*Model, error) {
	ort.SetSharedLibraryPath(ortLibPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("init onnxruntime: %w", err)
	}

	m := &Model{}
	var err error

	if _, e := os.Stat(dir + "/nemo128.onnx"); e == nil {
		m.preprocessor, err = ort.NewDynamicAdvancedSession(dir+"/nemo128.onnx",
			[]string{"waveforms", "waveforms_lens"},
			[]string{"features", "features_lens"}, nil)
		if err != nil {
			return nil, fmt.Errorf("load preprocessor: %w", err)
		}
	}

	m.encoder, err = ort.NewDynamicAdvancedSession(dir+"/encoder.int8.onnx",
		[]string{"audio_signal", "length"},
		[]string{"outputs", "encoded_lengths"}, nil)
	if err != nil {
		return nil, fmt.Errorf("load encoder: %w", err)
	}

	m.decoder, err = ort.NewDynamicAdvancedSession(dir+"/decoder.int8.onnx",
		[]string{"targets", "target_length", "states.1", "onnx::Slice_3"},
		[]string{"outputs", "prednet_lengths", "states", "162"}, nil)
	if err != nil {
		return nil, fmt.Errorf("load decoder: %w", err)
	}

	m.joiner, err = ort.NewDynamicAdvancedSession(dir+"/joiner.int8.onnx",
		[]string{"encoder_outputs", "decoder_outputs"},
		[]string{"outputs"}, nil)
	if err != nil {
		return nil, fmt.Errorf("load joiner: %w", err)
	}

	m.vocab, err = loadVocab(dir + "/tokens.txt")
	if err != nil {
		return nil, fmt.Errorf("load vocab: %w", err)
	}

	m.blankIdx = len(m.vocab) - 1
	for i, t := range m.vocab {
		if t == "<blk>" {
			m.blankIdx = i
			break
		}
	}

	return m, nil
}

// Transcribe takes float32 PCM audio at 16kHz and returns the transcript.
func (m *Model) Transcribe(samples []float32) (string, error) {
	var encOut ort.Value
	var encodedLen int64

	if m.preprocessor != nil {
		audioLen := int64(len(samples))
		wf, _ := ort.NewTensor(ort.NewShape(1, audioLen), samples)
		defer wf.Destroy()
		wl, _ := ort.NewTensor(ort.NewShape(1), []int64{audioLen})
		defer wl.Destroy()

		prepOut := []ort.Value{nil, nil}
		if err := m.preprocessor.Run([]ort.Value{wf, wl}, prepOut); err != nil {
			return "", fmt.Errorf("preprocessor: %w", err)
		}
		defer prepOut[0].Destroy()
		defer prepOut[1].Destroy()

		featLen := getInt64(prepOut[1])[0]

		// Per-feature normalization (required by the encoder)
		featData := getFloat32(prepOut[0])
		featShape := prepOut[0].GetShape()
		numFeats := featShape[1] // 128
		numFrames := featShape[2]
		normalizeFeatures(featData, numFeats, numFrames)

		// Re-create tensor with normalized data
		normFeat, _ := ort.NewTensor(ort.NewShape(featShape...), featData)
		defer normFeat.Destroy()

		el, _ := ort.NewTensor(ort.NewShape(1), []int64{featLen})
		defer el.Destroy()

		eOut := []ort.Value{nil, nil}
		if err := m.encoder.Run([]ort.Value{normFeat, el}, eOut); err != nil {
			return "", fmt.Errorf("encoder: %w", err)
		}
		defer eOut[1].Destroy()
		encOut = eOut[0]
		encodedLen = getInt64(eOut[1])[0]
	}
	defer encOut.Destroy()

	encShape := encOut.GetShape()
	encData := getFloat32(encOut)

	tokens, err := m.decodeTDT(encData, encShape, int(encodedLen))
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	return tokensToText(m.vocab, tokens), nil
}

func (m *Model) decodeTDT(encData []float32, encShape []int64, encodedLen int) ([]int, error) {
	vocabSize := len(m.vocab)

	var tokens []int

	states1 := make([]float32, 2*1*640)
	states2 := make([]float32, 2*1*640)

	// Initial decoder run with blank token
	decOut, newS1, newS2, err := m.runDecoder([]int32{int32(m.blankIdx)}, states1, states2)
	if err != nil {
		return nil, fmt.Errorf("initial decoder: %w", err)
	}
	copy(states1, newS1)
	copy(states2, newS2)

	t := 0
	for t < encodedLen {
		// Extract encoder frame [1, 1024, 1]
		frameData := make([]float32, encShape[1])
		for h := int64(0); h < encShape[1]; h++ {
			frameData[h] = encData[h*encShape[2]+int64(t)]
		}

		logits, err := m.runJoiner(frameData, encShape[1], decOut)
		if err != nil {
			return nil, fmt.Errorf("joiner t=%d: %w", t, err)
		}

		// TDT: separate argmax for token and duration
		bestToken := 0
		bestScore := logits[0]
		for i := 1; i < vocabSize; i++ {
			if logits[i] > bestScore {
				bestScore = logits[i]
				bestToken = i
			}
		}

		// Duration skip
		skip := 0
		bestDurScore := logits[vocabSize]
		for i := vocabSize + 1; i < len(logits); i++ {
			if logits[i] > bestDurScore {
				bestDurScore = logits[i]
				skip = i - vocabSize
			}
		}
		if skip == 0 {
			skip = 1
		}

		if bestToken != m.blankIdx {
			tokens = append(tokens, bestToken)
			copy(states1, newS1)
			copy(states2, newS2)
			decOut, newS1, newS2, err = m.runDecoder([]int32{int32(bestToken)}, states1, states2)
			if err != nil {
				return nil, fmt.Errorf("decoder t=%d: %w", t, err)
			}
		}

		t += skip
	}

	return tokens, nil
}

func (m *Model) runDecoder(targets []int32, s1, s2 []float32) ([]float32, []float32, []float32, error) {
	tgt, _ := ort.NewTensor(ort.NewShape(1, int64(len(targets))), targets)
	defer tgt.Destroy()
	tl, _ := ort.NewTensor(ort.NewShape(1), []int32{int32(len(targets))})
	defer tl.Destroy()
	st1, _ := ort.NewTensor(ort.NewShape(2, 1, 640), s1)
	defer st1.Destroy()
	st2, _ := ort.NewTensor(ort.NewShape(2, 1, 640), s2)
	defer st2.Destroy()

	dOut := []ort.Value{nil, nil, nil, nil}
	if err := m.decoder.Run([]ort.Value{tgt, tl, st1, st2}, dOut); err != nil {
		return nil, nil, nil, err
	}
	defer dOut[1].Destroy()

	out := copyF32(getFloat32(dOut[0]))
	ns1 := copyF32(getFloat32(dOut[2]))
	ns2 := copyF32(getFloat32(dOut[3]))

	dOut[0].Destroy()
	dOut[2].Destroy()
	dOut[3].Destroy()

	return out, ns1, ns2, nil
}

func (m *Model) runJoiner(encFrame []float32, hiddenDim int64, decOut []float32) ([]float32, error) {
	ef, _ := ort.NewTensor(ort.NewShape(1, hiddenDim, 1), encFrame)
	defer ef.Destroy()
	df, _ := ort.NewTensor(ort.NewShape(1, 640, 1), decOut)
	defer df.Destroy()

	jOut := []ort.Value{nil}
	if err := m.joiner.Run([]ort.Value{ef, df}, jOut); err != nil {
		return nil, err
	}
	result := copyF32(getFloat32(jOut[0]))
	jOut[0].Destroy()
	return result, nil
}

func tokensToText(vocab []string, tokens []int) string {
	var parts []string
	for _, t := range tokens {
		if t >= 0 && t < len(vocab) {
			tok := vocab[t]
			if strings.HasPrefix(tok, "<") && strings.HasSuffix(tok, ">") {
				continue
			}
			parts = append(parts, tok)
		}
	}
	text := strings.Join(parts, "")
	text = strings.ReplaceAll(text, "▁", " ")
	return strings.TrimSpace(text)
}

func loadVocab(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var vocab []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		lastSpace := strings.LastIndex(line, " ")
		if lastSpace < 0 {
			vocab = append(vocab, line)
			continue
		}
		vocab = append(vocab, line[:lastSpace])
	}
	return vocab, scanner.Err()
}

func getFloat32(v ort.Value) []float32 {
	if t, ok := v.(*ort.Tensor[float32]); ok {
		return t.GetData()
	}
	return nil
}

func getInt64(v ort.Value) []int64 {
	if t, ok := v.(*ort.Tensor[int64]); ok {
		return t.GetData()
	}
	return nil
}

func copyF32(src []float32) []float32 {
	dst := make([]float32, len(src))
	copy(dst, src)
	return dst
}

// normalizeFeatures applies per-feature mean/stddev normalization.
// Features layout: [1, numFeats, numFrames] stored row-major.
func normalizeFeatures(data []float32, numFeats, numFrames int64) {
	for f := int64(0); f < numFeats; f++ {
		// Compute mean
		var sum float64
		for t := int64(0); t < numFrames; t++ {
			sum += float64(data[f*numFrames+t])
		}
		mean := sum / float64(numFrames)

		// Compute stddev
		var sqSum float64
		for t := int64(0); t < numFrames; t++ {
			d := float64(data[f*numFrames+t]) - mean
			sqSum += d * d
		}
		stddev := math.Sqrt(sqSum/float64(numFrames)) + 1e-5

		// Normalize
		for t := int64(0); t < numFrames; t++ {
			data[f*numFrames+t] = float32((float64(data[f*numFrames+t]) - mean) / stddev)
		}
	}
}
