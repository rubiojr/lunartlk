# lunartlk-server

HTTP transcription server powered by Moonshine speech-to-text models. Accepts audio uploads and returns JSON transcripts.

## Usage

```bash
lunartlk-server [flags]
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-addr` | `:9765` | Listen address |
| `-lang` | `es` | Default language (`en`, `es`) |
| `-models` | auto | Models root directory |
| `-doctor` | | Run preflight checks and exit |

### Examples

```bash
# Start with default settings (Spanish, port 9765)
./bin/lunartlk-server

# Start with English as default
./bin/lunartlk-server -lang en

# Custom port
./bin/lunartlk-server -addr :8080

# Check dependencies
./bin/lunartlk-server -doctor
```

## API

### POST /transcribe

Transcribe an audio file. Accepts `.wav` (16-bit PCM) and `.opus` uploads.

**Query parameters:**

| Param | Default | Description |
|---|---|---|
| `lang` | server default | Language: `en` or `es` |

**Request:**

```bash
# WAV file
curl -F 'audio=@recording.wav' http://localhost:9765/transcribe

# Opus file
curl -F 'audio=@recording.opus' http://localhost:9765/transcribe

# Specify language
curl -F 'audio=@recording.wav' 'http://localhost:9765/transcribe?lang=en'
```

**Response:**

```json
{
  "text": "Full transcript joined together.",
  "lines": [
    {
      "text": "It was the best of times.",
      "start_time": 0.992,
      "duration": 3.936,
      "speaker": 0
    }
  ],
  "audio_duration": 44.374,
  "processing_ms": 3098,
  "model": "base-en",
  "lang": "en",
  "arch": 1
}
```

| Field | Description |
|---|---|
| `text` | Full transcript, all lines joined |
| `lines` | Individual speech segments with timestamps |
| `audio_duration` | Length of submitted audio in seconds |
| `processing_ms` | Inference time in milliseconds |
| `model` | Model name used |
| `lang` | Language used |
| `arch` | Model architecture ID |

### GET /health

Returns `ok` with status 200.

## How it works

1. The server is distributed as a **self-extracting binary** that bundles the Go binary, shared libraries (`libmoonshine.so`, `libonnxruntime.so`), and model files.
2. On first run, it extracts to a cache directory and re-executes with `LD_LIBRARY_PATH` set.
3. At startup, it loads all bundled models (one per language) into memory.
4. Each `/transcribe` request decodes the uploaded audio, runs it through the selected model's VAD + transcriber, and returns JSON.

## Storage

| Path | Description |
|---|---|
| `~/.cache/lunartlk/` | Extracted server binary, shared libraries, and models |
| `~/.cache/lunartlk/.extracted` | Hash marker — re-extraction only happens when the binary changes |

Override the cache directory with `LUNARTLK_CACHE_DIR` or `XDG_CACHE_HOME`.

## Models

The server loads models from `<extract_dir>/models/<model-name>/`. Each model directory contains ONNX model files and a tokenizer.

Models bundled by default:

| Model | Language | Architecture | License |
|---|---|---|---|
| `base-en` | English | 1 (base) | MIT |
| `base-es` | Spanish | 1 (base) | Moonshine Community License |

See [MODELS-LICENSE.md](../MODELS-LICENSE.md) for full license details.
