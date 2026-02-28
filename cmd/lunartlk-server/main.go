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
	"sync"
	"time"
	"unsafe"

	"github.com/rubiojr/lunartlk/internal/audio"
	"github.com/rubiojr/lunartlk/internal/doctor"
	mdl "github.com/rubiojr/lunartlk/internal/models"
	"github.com/rubiojr/lunartlk/internal/parakeet"
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
	Engine        string           `json:"engine"`
}

// transcriber abstracts over moonshine and parakeet engines.
type transcriber interface {
	Transcribe(samples []float32, sampleRate int32) (*TranscriptResponse, error)
}

// --- Moonshine engine ---

type moonshineTranscriber struct {
	handle    C.int32_t
	modelName string
}

func (m *moonshineTranscriber) Transcribe(samples []float32, sampleRate int32) (*TranscriptResponse, error) {
	var transcript *C.struct_transcript_t
	rc := C.moonshine_transcribe_without_streaming(
		m.handle,
		(*C.float)(unsafe.Pointer(&samples[0])),
		C.uint64_t(len(samples)),
		C.int32_t(sampleRate),
		0,
		&transcript,
	)
	if rc != 0 {
		return nil, fmt.Errorf("moonshine: %s", C.GoString(C.moonshine_error_to_string(rc)))
	}

	resp := &TranscriptResponse{
		Model:  m.modelName,
		Engine: "moonshine",
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
	return resp, nil
}

// --- Parakeet engine ---

type parakeetTranscriber struct {
	model *parakeet.Model
	mu    sync.Mutex // ONNX Runtime sessions aren't thread-safe
}

func (p *parakeetTranscriber) Transcribe(samples []float32, sampleRate int32) (*TranscriptResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	text, err := p.model.Transcribe(samples)
	if err != nil {
		return nil, fmt.Errorf("parakeet: %w", err)
	}
	return &TranscriptResponse{
		Text:   text,
		Model:  "parakeet-tdt-0.6b-v3",
		Engine: "parakeet",
	}, nil
}

// --- Lazy Moonshine loader ---

type lazyMoonshine struct {
	mu        sync.Mutex
	loaded    *moonshineTranscriber
	modelName string
	cacheDir  string
}

func (l *lazyMoonshine) Transcribe(samples []float32, sampleRate int32) (*TranscriptResponse, error) {
	l.mu.Lock()
	if l.loaded == nil {
		log.Printf("[moonshine] Loading %s on first request...", l.modelName)
		info := mdl.MoonshineModels[l.modelName]
		modelPath, err := mdl.EnsureModel(l.cacheDir, info)
		if err != nil {
			l.mu.Unlock()
			return nil, fmt.Errorf("download %s: %w", l.modelName, err)
		}
		cPath := C.CString(modelPath)
		handle := C.moonshine_load_transcriber_from_files(
			cPath, C.uint32_t(C.MOONSHINE_MODEL_ARCH_BASE), nil, 0, C.MOONSHINE_HEADER_VERSION,
		)
		C.free(unsafe.Pointer(cPath))
		if handle < 0 {
			l.mu.Unlock()
			return nil, fmt.Errorf("load %s: %s", l.modelName, C.GoString(C.moonshine_error_to_string(handle)))
		}
		l.loaded = &moonshineTranscriber{handle: handle, modelName: l.modelName}
		log.Printf("[moonshine] Loaded: %s", l.modelName)
	}
	t := l.loaded
	l.mu.Unlock()
	return t.Transcribe(samples, sampleRate)
}

// --- Lazy Parakeet loader ---

type lazyParakeet struct {
	mu       sync.Mutex
	loaded   *parakeetTranscriber
	cacheDir string
	ortPath  string
}

func (l *lazyParakeet) Transcribe(samples []float32, sampleRate int32) (*TranscriptResponse, error) {
	l.mu.Lock()
	if l.loaded == nil {
		log.Printf("[parakeet] Loading on first request...")
		pkDir, err := mdl.EnsureModel(l.cacheDir, mdl.ParakeetModel)
		if err != nil {
			l.mu.Unlock()
			return nil, fmt.Errorf("download parakeet: %w", err)
		}
		mdl.EnsureModel(l.cacheDir, mdl.ParakeetPreprocessor)
		pkModel, err := parakeet.LoadModel(pkDir, l.ortPath)
		if err != nil {
			l.mu.Unlock()
			return nil, fmt.Errorf("load parakeet: %w", err)
		}
		l.loaded = &parakeetTranscriber{model: pkModel}
		log.Printf("[parakeet] Loaded: parakeet-tdt-0.6b-v3")
	}
	t := l.loaded
	l.mu.Unlock()
	return t.Transcribe(samples, sampleRate)
}

// --- Server ---

type serverInfo struct {
	moonshine   map[string]transcriber
	parakeet    transcriber
	defaultLang string
	defaultEng  string
	debug       bool
	token       string
}

func main() {
	doctorFlag := flag.Bool("doctor", false, "run preflight checks and exit")
	debugFlag := flag.Bool("debug", false, "log transcript text in request logs")
	tokenFlag := flag.String("token", "", "require Bearer token for authentication")
	addr := flag.String("addr", ":9765", "listen address")
	lang := flag.String("lang", "es", "default language (en, es)")
	engine := flag.String("engine", "parakeet", "default engine (moonshine, parakeet)")
	cacheDir := flag.String("cache", "", "cache directory for models (default: ~/.cache/lunartlk)")
	ortLib := flag.String("ort", "", "ONNX Runtime library path (default: auto-detect)")
	flag.Parse()

	if *doctorFlag {
		fmt.Fprintln(os.Stderr, "lunartlk-server preflight checks:")
		results := doctor.RunChecks("server")
		if doctor.PrintResults(results) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	cache := *cacheDir
	if cache == "" {
		if d := os.Getenv("_MOONSHINE_DIR"); d != "" {
			cache = d
		} else if d := os.Getenv("LUNARTLK_CACHE_DIR"); d != "" {
			cache = d
		} else if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
			cache = filepath.Join(d, "lunartlk")
		} else {
			home, _ := os.UserHomeDir()
			cache = filepath.Join(home, ".cache", "lunartlk")
		}
	}

	srv := serverInfo{
		moonshine:   make(map[string]transcriber),
		defaultLang: *lang,
		defaultEng:  *engine,
		debug:       *debugFlag,
		token:       *tokenFlag,
	}

	// Register lazy Moonshine models
	for langCode, modelName := range map[string]string{"es": "base-es", "en": "base-en"} {
		srv.moonshine[langCode] = &lazyMoonshine{modelName: modelName, cacheDir: cache}
		log.Printf("[moonshine] Registered: %s (%s, lazy)", modelName, langCode)
	}

	// Register lazy Parakeet model
	ortPath := *ortLib
	if ortPath == "" {
		for _, p := range []string{
			filepath.Join(cache, "libs", "libonnxruntime.so.1"),
			"third-party/moonshine/onnxruntime/libonnxruntime.so.1",
		} {
			if _, err := os.Stat(p); err == nil {
				ortPath = p
				break
			}
		}
	}
	if ortPath != "" {
		srv.parakeet = &lazyParakeet{cacheDir: cache, ortPath: ortPath}
		log.Printf("[parakeet] Registered: parakeet-tdt-0.6b-v3 (lazy)")
	} else {
		log.Printf("[parakeet] No ONNX Runtime found, skipping")
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

	var engines []string
	if len(srv.moonshine) > 0 {
		var langs []string
		for k := range srv.moonshine {
			langs = append(langs, k)
		}
		engines = append(engines, fmt.Sprintf("moonshine(%s)", strings.Join(langs, ",")))
	}
	if srv.parakeet != nil {
		engines = append(engines, "parakeet(multilingual)")
	}
	log.Printf("lunartlk server listening on %s [engines: %s, default: %s/%s, lazy loading]",
		*addr, strings.Join(engines, " "), srv.defaultEng, srv.defaultLang)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func handleTranscribe(w http.ResponseWriter, r *http.Request, srv *serverInfo) {
	if srv.token != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+srv.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)

	langCode := r.URL.Query().Get("lang")
	if langCode == "" {
		langCode = srv.defaultLang
	}
	engineName := r.URL.Query().Get("engine")
	if engineName == "" {
		engineName = srv.defaultEng
	}

	// Select transcriber
	var t transcriber
	switch engineName {
	case "parakeet":
		if srv.parakeet == nil {
			http.Error(w, "parakeet engine not loaded", http.StatusBadRequest)
			return
		}
		t = srv.parakeet
	case "moonshine":
		t = srv.moonshine[langCode]
		if t == nil {
			var avail []string
			for k := range srv.moonshine {
				avail = append(avail, k)
			}
			http.Error(w, fmt.Sprintf("moonshine: unknown lang '%s', available: %s", langCode, strings.Join(avail, ", ")),
				http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, fmt.Sprintf("unknown engine '%s', use 'moonshine' or 'parakeet'", engineName), http.StatusBadRequest)
		return
	}

	// Decode audio
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
		samples, sampleRate, err = audio.DecodeWAV(data)
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

	// Transcribe
	startTime := time.Now()
	resp, err := t.Transcribe(samples, sampleRate)
	if err != nil {
		http.Error(w, "transcription failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	processingMs := time.Since(startTime).Milliseconds()

	resp.AudioDuration = math.Round(audioDuration*1000) / 1000
	resp.ProcessingMs = processingMs
	resp.Lang = langCode

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)

	if srv.debug {
		logText := resp.Text
		if len(logText) > 80 {
			logText = logText[:80] + "..."
		}
		log.Printf("%s engine=%s lang=%s fmt=%s audio=%.1fs proc=%dms text=%q",
			r.RemoteAddr, engineName, langCode, filepath.Ext(name), audioDuration, processingMs, logText)
	} else {
		log.Printf("%s engine=%s lang=%s fmt=%s audio=%.1fs proc=%dms",
			r.RemoteAddr, engineName, langCode, filepath.Ext(name), audioDuration, processingMs)
	}
}
