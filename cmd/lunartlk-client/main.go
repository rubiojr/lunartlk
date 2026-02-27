package main

// #cgo pkg-config: portaudio-2.0
// #cgo LDFLAGS: -Wl,--as-needed -Wl,--disable-new-dtags
// #cgo linux,amd64 LDFLAGS: -L/usr/lib64/pipewire-0.3/jack -Wl,-rpath,/usr/lib64/pipewire-0.3/jack
// #cgo linux,arm64 LDFLAGS: -L/usr/lib/aarch64-linux-gnu/pipewire-0.3/jack -Wl,-rpath,/usr/lib/aarch64-linux-gnu/pipewire-0.3/jack
import "C"

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"lunartlk/internal/audio"
	"lunartlk/internal/doctor"

	"lunartlk/internal/wav"

	"github.com/gordonklaus/portaudio"
)

const sampleRate = 16000

type TranscriptLine struct {
	Text      string  `json:"text"`
	StartTime float64 `json:"start_time"`
	Duration  float64 `json:"duration"`
}

type TranscriptResponse struct {
	Text          string           `json:"text"`
	Lines         []TranscriptLine `json:"lines"`
	AudioDuration float64          `json:"audio_duration"`
	ProcessingMs  int64            `json:"processing_ms"`
	Model         string           `json:"model"`
	Lang          string           `json:"lang"`
	Engine        string           `json:"engine"`
	Arch          int              `json:"arch"`
}

func main() {
	doctorFlag := flag.Bool("doctor", false, "run preflight checks and exit")
	server := flag.String("server", "http://localhost:9765", "transcription server URL")
	token := flag.String("token", "", "Bearer token for server authentication")
	lang := flag.String("lang", "", "language for transcription (en, es)")
	engineFlag := flag.String("engine", "", "transcription engine (moonshine, parakeet)")
	clipboard := flag.Bool("clipboard", false, "copy result to clipboard via wl-copy")
	noSave := flag.Bool("no-save", false, "don't save transcript to disk")
	saveWav := flag.String("save-wav", "", "save recorded audio to this WAV file for debugging")
	flag.Parse()

	if *doctorFlag {
		fmt.Fprintln(os.Stderr, "lunartlk-client preflight checks:")
		results := doctor.RunChecks("client")
		if doctor.PrintResults(results) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	if err := portaudio.Initialize(); err != nil {
		log.Fatalf("PortAudio init failed: %v", err)
	}

	chunkSize := 1024
	buf := make([]float32, chunkSize)
	var recorded []float32

	stream, err := portaudio.OpenDefaultStream(1, 0, float64(sampleRate), chunkSize, buf)
	if err != nil {
		log.Fatalf("Failed to open mic: %v", err)
	}

	if err := stream.Start(); err != nil {
		log.Fatalf("Failed to start mic: %v", err)
	}

	fmt.Fprintln(os.Stderr, "🎙  Recording... press Ctrl+C to stop and transcribe")

	stopped := make(chan struct{})
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		signal.Stop(c)
		close(stopped)
	}()

	start := time.Now()
	lastPrint := start

	for {
		select {
		case <-stopped:
			goto done
		default:
		}

		if err := stream.Read(); err != nil {
			break
		}
		chunk := make([]float32, chunkSize)
		copy(chunk, buf)
		recorded = append(recorded, chunk...)

		if time.Since(lastPrint) >= 100*time.Millisecond {
			elapsed := time.Since(start).Truncate(100 * time.Millisecond)
			fmt.Fprintf(os.Stderr, "\r⏱  %s", elapsed)
			lastPrint = time.Now()
		}
	}
done:

	stream.Stop()
	stream.Close()
	portaudio.Terminate()

	// Pad 1s of silence so the model doesn't clip the last word
	pad := make([]float32, sampleRate)
	recorded = append(recorded, pad...)

	elapsed := time.Since(start).Truncate(time.Millisecond)
	fmt.Fprintf(os.Stderr, "\r⏹  Recorded %s (%d samples)\n", elapsed, len(recorded))

	if len(recorded) == 0 {
		fmt.Fprintln(os.Stderr, "Nothing recorded.")
		return
	}

	// Normalize audio volume
	normalizeAudio(recorded)

	// Encode normalized audio as Opus
	opusEnc, err := audio.NewStreamEncoder(64000)
	if err != nil {
		log.Fatalf("Opus encoder init failed: %v", err)
	}
	opusEnc.Write(recorded)
	opusEnc.Flush()

	// Save backup WAV before sending
	wavData := wav.Encode(recorded, sampleRate)
	backupPath := filepath.Join(os.TempDir(), fmt.Sprintf("lunartlk-%d.wav", time.Now().Unix()))
	if err := os.WriteFile(backupPath, wavData, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  Failed to save backup: %v\n", err)
	}

	if *saveWav != "" {
		if err := os.WriteFile(*saveWav, wavData, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "⚠  Failed to save WAV: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "💾 Saved to %s\n", *saveWav)
		}
	}

	opusData := opusEnc.Bytes()
	oggData := opusEnc.OggBytes()
	fmt.Fprintf(os.Stderr, "🔊 Encoded: %dKB WAV → %dKB Opus\n", len(wavData)/1024, len(opusData)/1024)

	serverURL := strings.TrimRight(*server, "/")
	transcribeURL := serverURL + "/transcribe"
	var params []string
	if *lang != "" {
		params = append(params, "lang="+*lang)
	}
	if *engineFlag != "" {
		params = append(params, "engine="+*engineFlag)
	}
	if len(params) > 0 {
		transcribeURL += "?" + strings.Join(params, "&")
	}

	fmt.Fprintln(os.Stderr, "📡 Sending to server...")
	resp, err := sendToServer(transcribeURL, opusData, "recording.opus", *token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠  Server error: %v\n", err)
		fmt.Fprintf(os.Stderr, "💾 Audio saved at: %s\n", backupPath)
		os.Exit(1)
	}

	// Success — remove backup
	os.Remove(backupPath)

	// Save transcript and audio
	if !*noSave {
		saveTranscript(resp)
		saveAudio(oggData)
	}

	if resp.Text == "" {
		fmt.Fprintln(os.Stderr, "No speech detected.")
		return
	}

	fmt.Fprintf(os.Stderr, "\n[%s/%s, lang=%s, %.1fs audio, %dms processing]\n",
		resp.Engine, resp.Model, resp.Lang, resp.AudioDuration, resp.ProcessingMs)
	fmt.Println(resp.Text)

	if *clipboard {
		copyToClipboard(resp.Text)
	}
}

func sendToServer(url string, data []byte, filename string, token string) (*TranscriptResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("audio", filename)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("write audio: %w", err)
	}
	writer.Close()

	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(b))
	}

	var result TranscriptResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

func copyToClipboard(text string) {
	cmd := exec.Command("wl-copy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  wl-copy failed: %v\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "📋 Copied to clipboard")
}

func dataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "lunartlk")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "lunartlk")
}

func saveTranscript(resp *TranscriptResponse) {
	dir := filepath.Join(dataDir(), "transcripts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  Failed to create transcript dir: %v\n", err)
		return
	}

	filename := time.Now().Format("2006-01-02T15-04-05") + ".json"
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠  Failed to marshal transcript: %v\n", err)
		return
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  Failed to save transcript: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "📝 Transcript saved to %s\n", path)
}

func saveAudio(opusData []byte) {
	dir := filepath.Join(dataDir(), "audio")
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  Failed to create audio dir: %v\n", err)
		return
	}

	filename := time.Now().Format("2006-01-02T15-04-05") + ".opus"
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, opusData, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  Failed to save audio: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "🔊 Audio saved to %s\n", path)
}

func normalizeAudio(samples []float32) {
	var peak float32
	for _, s := range samples {
		if s > peak {
			peak = s
		} else if -s > peak {
			peak = -s
		}
	}
	if peak < 0.001 {
		return
	}
	gain := float32(0.9) / peak
	fmt.Fprintf(os.Stderr, "🔈 Peak: %.3f, gain: %.1fx\n", peak, gain)
	for i := range samples {
		samples[i] *= gain
	}
}
