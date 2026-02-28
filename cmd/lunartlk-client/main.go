package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"lunartlk/client"
	"lunartlk/internal/audio"
	"lunartlk/internal/doctor"
	"lunartlk/translate"
)

const sampleRate = 16000

func main() {
	doctorFlag := flag.Bool("doctor", false, "run preflight checks and exit")
	server := flag.String("server", "http://localhost:9765", "transcription server URL")
	token := flag.String("token", "", "Bearer token for server authentication")
	lang := flag.String("lang", "", "language for transcription (en, es)")
	engineFlag := flag.String("engine", "", "transcription engine (moonshine, parakeet)")
	clipboard := flag.Bool("clipboard", false, "copy result to clipboard via wl-copy")
	noSave := flag.Bool("no-save", false, "don't save transcript to disk")
	saveWav := flag.String("save-wav", "", "save recorded audio to this WAV file for debugging")
	translateTo := flag.String("translate", "", "translate transcript to language (e.g. English, Spanish)")
	ollamaModel := flag.String("ollama-model", "lfm2", "Ollama model for translation")
	ollamaHost := flag.String("ollama-host", "", "Ollama server URL (default: $OLLAMA_HOST or http://localhost:11434)")
	flag.Parse()

	if *doctorFlag {
		fmt.Fprintln(os.Stderr, "lunartlk-client preflight checks:")
		results := doctor.RunChecks("client")
		if doctor.PrintResults(results) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	rec, err := client.NewRecorder(sampleRate, 1024)
	if err != nil {
		log.Fatalf("Recorder init failed: %v", err)
	}
	defer rec.Close()

	if err := rec.Start(); err != nil {
		log.Fatalf("Failed to start recording: %v", err)
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
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-stopped:
			break loop
		case <-ticker.C:
			elapsed := time.Since(start).Truncate(100 * time.Millisecond)
			fmt.Fprintf(os.Stderr, "\r⏱  %s", elapsed)
		}
	}

	recorded := rec.Stop()

	// Pad 1s of silence so the model doesn't clip the last word
	pad := make([]float32, sampleRate)
	recorded = append(recorded, pad...)

	elapsed := time.Since(start).Truncate(time.Millisecond)
	fmt.Fprintf(os.Stderr, "\r⏹  Recorded %s (%d samples)\n", elapsed, len(recorded))

	if len(recorded) == 0 {
		fmt.Fprintln(os.Stderr, "Nothing recorded.")
		return
	}

	peak, gain := client.NormalizeAudio(recorded)
	fmt.Fprintf(os.Stderr, "🔈 Peak: %.3f, gain: %.1fx\n", peak, gain)

	// Encode normalized audio as Opus
	opusEnc, err := audio.NewStreamEncoder(64000)
	if err != nil {
		log.Fatalf("Opus encoder init failed: %v", err)
	}
	opusEnc.Write(recorded)
	opusEnc.Flush()

	// Save backup WAV before sending
	wavData := audio.EncodeWAV(recorded, sampleRate)
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

	var opts []client.Option
	if *token != "" {
		opts = append(opts, client.WithToken(*token))
	}
	if *lang != "" {
		opts = append(opts, client.WithLang(*lang))
	}
	if *engineFlag != "" {
		opts = append(opts, client.WithEngine(*engineFlag))
	}
	tc := client.New(*server, opts...)

	fmt.Fprintln(os.Stderr, "📡 Sending to server...")
	resp, err := tc.Transcribe(opusData, "recording.opus")
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

	output := resp.Text
	if *translateTo != "" {
		fmt.Fprintf(os.Stderr, "🌐 Translating to %s...\n", *translateTo)
		var trOpts []translate.OllamaOption
		trOpts = append(trOpts, translate.WithModel(*ollamaModel))
		if *ollamaHost != "" {
			trOpts = append(trOpts, translate.WithHost(*ollamaHost))
		}
		tr := translate.NewOllama(trOpts...)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		translated, err := tr.Translate(ctx, output, *translateTo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠  Translation failed: %v\n", err)
		} else {
			output = translated
		}
	}

	fmt.Println(output)

	if *clipboard {
		copyToClipboard(output)
	}
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

func saveTranscript(resp *client.TranscriptResponse) {
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
