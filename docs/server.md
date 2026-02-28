# lunartlk-server

HTTP transcription server with two engines: Moonshine (fast, English/Spanish) and Parakeet v3 (25 languages, highest accuracy). Models download automatically on first use and are loaded lazily to minimize memory.

## Usage

```bash
lunartlk-server [flags]
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-addr` | `:9765` | Listen address |
| `-engine` | `parakeet` | Default engine (`moonshine`, `parakeet`) |
| `-lang` | `es` | Default language (`en`, `es`) |
| `-token` | | Require Bearer token for authentication |
| `-cache` | `~/.cache/lunartlk` | Cache directory for models |
| `-ort` | auto | ONNX Runtime library path |
| `-debug` | `false` | Log transcript text in request logs |
| `-doctor` | | Run preflight checks and exit |

### Examples

```bash
# Start with defaults (Parakeet, Spanish, port 9765)
./bin/lunartlk-server

# Use Parakeet as default engine
./bin/lunartlk-server -engine parakeet

# English default with auth
./bin/lunartlk-server -lang en -token mysecret

# Check dependencies
./bin/lunartlk-server -doctor
```

## Engines

### Moonshine

Fast, lightweight speech-to-text via the Moonshine C library. Separate models per language.

| Model | Language | Size | License |
|---|---|---|---|
| `base-en` | English | ~135MB | MIT |
| `base-es` | Spanish | ~62MB | Moonshine Community License |

### Parakeet v3

NVIDIA's Parakeet-TDT-0.6B-V3 via ONNX Runtime. Single model, 25 European languages, highest accuracy (WER ~2.1%).

| Model | Languages | Size | License |
|---|---|---|---|
| `parakeet-tdt-0.6b-v3` | 25 (en, es, de, fr, ...) | ~640MB | CC BY 4.0 |

## API

### POST /transcribe

Transcribe an audio file. Accepts `.wav` (16-bit PCM) and `.opus` uploads.

**Query parameters:**

| Param | Default | Description |
|---|---|---|
| `engine` | server default | Engine: `moonshine` or `parakeet` |
| `lang` | server default | Language: `en`, `es` (moonshine only) |

**Request:**

```bash
# Default engine
curl -F 'audio=@recording.wav' http://localhost:9765/transcribe

# Parakeet engine
curl -F 'audio=@recording.wav' 'http://localhost:9765/transcribe?engine=parakeet'

# Moonshine English
curl -F 'audio=@recording.wav' 'http://localhost:9765/transcribe?engine=moonshine&lang=en'

# With authentication
curl -H "Authorization: Bearer mysecret" -F 'audio=@recording.wav' http://localhost:9765/transcribe
```

**Response:**

```json
{
  "text": "Ask not what your country can do for you.",
  "lines": [
    {
      "text": "Ask not what your country can do for you.",
      "start_time": 0.0,
      "duration": 3.84,
      "speaker": 0
    }
  ],
  "audio_duration": 3.845,
  "processing_ms": 260,
  "model": "parakeet-tdt-0.6b-v3",
  "lang": "en",
  "engine": "parakeet"
}
```

| Field | Description |
|---|---|
| `text` | Full transcript, all lines joined |
| `lines` | Individual speech segments with timestamps (moonshine only) |
| `audio_duration` | Length of submitted audio in seconds |
| `processing_ms` | Inference time in milliseconds |
| `model` | Model name used |
| `lang` | Language used |
| `engine` | Engine used (`moonshine` or `parakeet`) |

### GET /health

Returns `ok` with status 200. Not affected by authentication.

## Authentication

When started with `-token`, all `/transcribe` requests require a `Bearer` token in the `Authorization` header. The `/health` endpoint is always open.

## How it works

1. The server binary bundles shared libraries (`libmoonshine.so`, `libonnxruntime.so`) in a self-extracting wrapper.
2. On first run, libraries extract to `~/.cache/lunartlk/`.
3. Models are **not bundled** — they download automatically on first request for each engine/language.
4. Models are **lazy-loaded** — only the engine you actually use consumes RAM.
5. Subsequent starts are instant (cached libraries + models).

## Storage

| Path | Description |
|---|---|
| `~/.cache/lunartlk/libs/` | Extracted shared libraries |
| `~/.cache/lunartlk/models/base-en/` | Moonshine English model |
| `~/.cache/lunartlk/models/base-es/` | Moonshine Spanish model |
| `~/.cache/lunartlk/models/parakeet-v3-sherpa/` | Parakeet v3 model (encoder, decoder, joiner) |
| `~/.cache/lunartlk/.extracted` | Hash marker for library extraction |

Override the cache directory with `-cache`, `LUNARTLK_CACHE_DIR`, or `XDG_CACHE_HOME`.

## Model Licenses

See [MODELS-LICENSE.md](../MODELS-LICENSE.md) for full details.

- **Moonshine English**: MIT
- **Moonshine Spanish**: Moonshine Community License (non-commercial)
- **Parakeet v3**: CC BY 4.0 (commercial OK with attribution)
