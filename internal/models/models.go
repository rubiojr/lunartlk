package models

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

type ModelInfo struct {
	Name    string
	BaseURL string
	Files   []string
}

var MoonshineModels = map[string]ModelInfo{
	"base-es": {
		Name:    "base-es",
		BaseURL: "https://download.moonshine.ai/model/base-es/quantized/base-es",
		Files:   []string{"encoder_model.ort", "decoder_model_merged.ort", "tokenizer.bin"},
	},
	"base-en": {
		Name:    "base-en",
		BaseURL: "https://download.moonshine.ai/model/base-en/quantized/base-en",
		Files:   []string{"encoder_model.ort", "decoder_model_merged.ort", "tokenizer.bin"},
	},
}

var ParakeetModel = ModelInfo{
	Name:    "parakeet-v3-sherpa",
	BaseURL: "https://huggingface.co/csukuangfj/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8/resolve/main",
	Files:   []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"},
}

var ParakeetPreprocessor = ModelInfo{
	Name:    "parakeet-v3-sherpa",
	BaseURL: "https://huggingface.co/istupakov/parakeet-tdt-0.6b-v3-onnx/resolve/main",
	Files:   []string{"nemo128.onnx"},
}

// EnsureModel downloads model files if they don't exist in dir.
// Returns the model directory path.
func EnsureModel(cacheDir string, info ModelInfo) (string, error) {
	dir := filepath.Join(cacheDir, "models", info.Name)

	// Check if all files exist
	allPresent := true
	for _, f := range info.Files {
		if _, err := os.Stat(filepath.Join(dir, f)); os.IsNotExist(err) {
			allPresent = false
			break
		}
	}
	if allPresent {
		return dir, nil
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create dir %s: %w", dir, err)
	}

	for _, f := range info.Files {
		dest := filepath.Join(dir, f)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		url := info.BaseURL + "/" + f
		log.Printf("Downloading %s/%s...", info.Name, f)
		if err := downloadFile(url, dest); err != nil {
			return "", fmt.Errorf("download %s: %w", f, err)
		}
	}

	return dir, nil
}

func downloadFile(url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	written, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}

	log.Printf("  Downloaded %s (%.1f MB)", filepath.Base(dest), float64(written)/1024/1024)
	return os.Rename(tmp, dest)
}
