package main

/*
#cgo CFLAGS: -I${SRCDIR}/../../third-party/moonshine/core
#cgo LDFLAGS: -L${SRCDIR}/../../third-party/moonshine/core/build -lmoonshine
#cgo LDFLAGS: -L${SRCDIR}/../../third-party/moonshine/onnxruntime -Wl,-rpath,${SRCDIR}/../../third-party/moonshine/onnxruntime

#include "moonshine-c-api.h"
#include <stdlib.h>
*/
import "C"
import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"lunartlk/internal/audio"
	"lunartlk/internal/doctor"
	"lunartlk/internal/wav"
)

type TranscriptLine struct {
	Text      string  `json:"text"`
	StartTime float64 `json:"start_time"`
	Duration  float64 `json:"duration"`
	Speaker   uint32  `json:"speaker"`
}

type TranscriptResponse struct {
	Text          string           `json:"text"`
	Lines         []TranscriptLine `json:"lines"`
	AudioDuration float64          `json:"audio_duration"`
	ProcessingMs  int64            `json:"processing_ms"`
	Model         string           `json:"model"`
	Lang          string           `json:"lang"`
	Arch          int              `json:"arch"`
}

type langConfig struct {
	modelDir  string
	modelArch int
	modelName string
}

type serverInfo struct {
	langs       map[string]*loadedLang
	defaultLang string
}

type loadedLang struct {
	handle    C.int32_t
	modelName string
	modelArch int
}

var defaultLangs = map[string]langConfig{
	"es": {modelDir: "models/base-es", modelArch: int(C.MOONSHINE_MODEL_ARCH_BASE), modelName: "base-es"},
	"en": {modelDir: "models/base-en", modelArch: int(C.MOONSHINE_MODEL_ARCH_BASE), modelName: "base-en"},
}

func main() {
	doctorFlag := flag.Bool("doctor", false, "run preflight checks and exit")
	addr := flag.String("addr", ":9765", "listen address")
	lang := flag.String("lang", "es", "default language (en, es)")
	modelsRoot := flag.String("models", "", "models root directory (default: auto-detect from _MOONSHINE_DIR)")
	flag.Parse()

	if *doctorFlag {
		fmt.Fprintln(os.Stderr, "lunartlk-server preflight checks:")
		results := doctor.RunChecks("server")
		if doctor.PrintResults(results) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	root := *modelsRoot
	if root == "" {
		if d := os.Getenv("_MOONSHINE_DIR"); d != "" {
			root = d
		} else {
			log.Fatal("No models root specified. Use -models <path> or set _MOONSHINE_DIR")
		}
	}

	srv := serverInfo{
		langs:       make(map[string]*loadedLang),
		defaultLang: *lang,
	}

	for langCode, cfg := range defaultLangs {
		modelPath := filepath.Join(root, cfg.modelDir)
		if _, err := os.Stat(modelPath); os.IsNotExist(err) {
			log.Printf("Model for '%s' not found at %s, skipping", langCode, modelPath)
			continue
		}

		cPath := C.CString(modelPath)
		handle := C.moonshine_load_transcriber_from_files(
			cPath,
			C.uint32_t(cfg.modelArch),
			nil, 0,
			C.MOONSHINE_HEADER_VERSION,
		)
		C.free(unsafe.Pointer(cPath))

		if handle < 0 {
			log.Printf("Failed to load '%s' model: %s", langCode, C.GoString(C.moonshine_error_to_string(handle)))
			continue
		}

		srv.langs[langCode] = &loadedLang{
			handle:    handle,
			modelName: cfg.modelName,
			modelArch: cfg.modelArch,
		}
		log.Printf("Loaded model: %s (%s)", cfg.modelName, langCode)
	}

	if len(srv.langs) == 0 {
		log.Fatal("No models loaded")
	}
	if srv.langs[srv.defaultLang] == nil {
		// Fall back to first available
		for k := range srv.langs {
			srv.defaultLang = k
			break
		}
		log.Printf("Requested default '%s' not available, using '%s'", *lang, srv.defaultLang)
	}

	http.HandleFunc("/transcribe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		handleTranscribe(w, r, &srv)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	var loaded []string
	for k := range srv.langs {
		loaded = append(loaded, k)
	}
	log.Printf("lunartlk server listening on %s (languages: %s, default: %s)",
		*addr, strings.Join(loaded, ", "), srv.defaultLang)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func handleTranscribe(w http.ResponseWriter, r *http.Request, srv *serverInfo) {
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)

	// Language selection: ?lang=en or ?lang=es, defaults to server default
	langCode := r.URL.Query().Get("lang")
	if langCode == "" {
		langCode = srv.defaultLang
	}
	ll := srv.langs[langCode]
	if ll == nil {
		var avail []string
		for k := range srv.langs {
			avail = append(avail, k)
		}
		http.Error(w, fmt.Sprintf("unknown language '%s', available: %s", langCode, strings.Join(avail, ", ")),
			http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("audio")
	if err != nil {
		http.Error(w, "missing 'audio' form file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read upload: "+err.Error(), http.StatusBadRequest)
		return
	}

	name := strings.ToLower(header.Filename)

	var samples []float32
	var sampleRate int32

	switch {
	case strings.HasSuffix(name, ".wav"):
		samples, sampleRate, err = wav.Decode(data)
	case strings.HasSuffix(name, ".opus"):
		samples, sampleRate, err = audio.DecodeOpus(data)
	default:
		http.Error(w, "unsupported format, send .wav or .opus", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "failed to decode audio: "+err.Error(), http.StatusBadRequest)
		return
	}

	audioDuration := float64(len(samples)) / float64(sampleRate)

	startTime := time.Now()

	var transcript *C.struct_transcript_t
	rc := C.moonshine_transcribe_without_streaming(
		ll.handle,
		(*C.float)(unsafe.Pointer(&samples[0])),
		C.uint64_t(len(samples)),
		C.int32_t(sampleRate),
		0,
		&transcript,
	)
	if rc != 0 {
		http.Error(w, "transcription failed: "+C.GoString(C.moonshine_error_to_string(rc)),
			http.StatusInternalServerError)
		return
	}

	processingMs := time.Since(startTime).Milliseconds()

	resp := TranscriptResponse{
		AudioDuration: math.Round(audioDuration*1000) / 1000,
		ProcessingMs:  processingMs,
		Model:         ll.modelName,
		Lang:          langCode,
		Arch:          ll.modelArch,
	}

	var texts []string
	if transcript != nil && transcript.line_count > 0 {
		lines := unsafe.Slice(transcript.lines, transcript.line_count)
		for _, line := range lines {
			text := C.GoString(line.text)
			resp.Lines = append(resp.Lines, TranscriptLine{
				Text:      text,
				StartTime: math.Round(float64(line.start_time)*1000) / 1000,
				Duration:  math.Round(float64(line.duration)*1000) / 1000,
				Speaker:   uint32(line.speaker_index),
			})
			if text != "" {
				texts = append(texts, text)
			}
		}
	}
	resp.Text = strings.Join(texts, " ")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
