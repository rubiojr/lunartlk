# lunartlk-client

Microphone capture client that records audio, encodes it as Opus, and sends it to a lunartlk-server for transcription.

## Usage

```bash
lunartlk-client [flags]
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-server` | `http://localhost:9765` | Server URL |
| `-token` | | Bearer token for server authentication |
| `-engine` | | Engine override (`moonshine`, `parakeet`). Uses server default if omitted |
| `-lang` | | Language override (`en`, `es`). Uses server default if omitted |
| `-translate` | | Translate transcript to a language (e.g. `English`, `Spanish`). Requires Ollama |
| `-ollama-model` | `lfm2` | Ollama model for translation |
| `-ollama-host` | `$OLLAMA_HOST` or `http://localhost:11434` | Ollama server URL |
| `-clipboard` | `false` | Copy transcript (or translation) to clipboard via `wl-copy` |
| `-no-save` | `false` | Don't save transcript JSON and audio to disk |
| `-save-wav` | | Save recorded audio to a WAV file (for debugging) |
| `-doctor` | | Run preflight checks and exit |

### Examples

```bash
# Record and transcribe (default server/engine)
./bin/lunartlk-client

# Use Parakeet engine (25 languages, best accuracy)
./bin/lunartlk-client -engine parakeet

# Moonshine English
./bin/lunartlk-client -engine moonshine -lang en

# With authentication
./bin/lunartlk-client -server http://myserver:9765 -token mysecret

# Copy result to Wayland clipboard
./bin/lunartlk-client -clipboard

# Translate transcript to English
./bin/lunartlk-client -translate English

# Translate to Spanish using a specific model
./bin/lunartlk-client -translate Spanish -ollama-model llama3

# Translate using a remote Ollama host
./bin/lunartlk-client -translate English -ollama-host http://myhost:11434

# Save audio for debugging
./bin/lunartlk-client -save-wav /tmp/debug.wav

# Check dependencies
./bin/lunartlk-client -doctor
```

## How it works

1. Opens the default microphone via PortAudio at 16kHz mono.
2. Records audio in 1024-sample chunks (~64ms each).
3. Each chunk is Opus-encoded in real-time (no delay at end of recording).
4. A backup WAV file is saved to `/tmp/` before sending.
5. Sends the Opus-encoded audio to the server's `/transcribe` endpoint.
6. On success, prints the transcript and removes the backup. On failure, prints the backup path so no audio is lost.
7. If `-translate` is set, the transcript is sent to Ollama for translation before printing.

### Recording flow

```
Ctrl+C
  │
  ▼
[mic] ──→ [PCM buffer] ──→ [Opus encoder (real-time)]
              │                        │
              ▼                        ▼
         [backup WAV]            [POST /transcribe]
         /tmp/lunartlk-*.wav          │
              │                        ▼
              │                  [JSON response]
              │                        │
         (deleted on success)    [print transcript]
                                       │
                                       ▼ (if -translate)
                                 [Ollama translation]
                                       │
                                       ▼
                                 [save JSON transcript]
                                 ~/.local/share/lunartlk/transcripts/
                                       │
                                       ▼
                                 [save Opus audio]
                                 ~/.local/share/lunartlk/audio/
```

## Storage

| Path | Description |
|---|---|
| `~/.local/share/lunartlk/transcripts/` | Saved transcripts as timestamped JSON files |
| `~/.local/share/lunartlk/audio/` | Saved Opus-encoded audio files |
| `/tmp/lunartlk-<timestamp>.wav` | Backup WAV of last recording. Deleted on successful transcription. |

The data directory respects `XDG_DATA_HOME`. Files use the format `<YYYY-MM-DDThh-mm-ss>.json` and `<YYYY-MM-DDThh-mm-ss>.opus`.

Use `-no-save` to disable saving transcripts and audio.

The backup WAV uses the pattern `/tmp/lunartlk-<unix-timestamp>.wav`. If the server fails, the error message includes the full path:

```
⚠  Server error: connection refused
💾 Audio saved at: /tmp/lunartlk-1709048400.wav
```

You can manually send the backup later:

```bash
curl -F 'audio=@/tmp/lunartlk-1709048400.wav' http://localhost:9765/transcribe
```

## Output

Status messages go to stderr, transcript goes to stdout. This means you can pipe the transcript:

```bash
# Save transcript to file
./bin/lunartlk-client > transcript.txt

# Pipe to another command
./bin/lunartlk-client | tee transcript.txt
```

### Example session

```
🎙  Recording... press Ctrl+C to stop and transcribe
⏹  Recorded 5.2s (83200 samples)
🔊 Encoded: 162KB WAV → 10KB Opus
📡 Sending to server...

[base-es, lang=es, 6.2s audio, 1250ms processing]
Hola, ¿qué tal? ¿Cómo estás?
```

### Example session with translation

```
🎙  Recording... press Ctrl+C to stop and transcribe
⏹  Recorded 3.6s (57600 samples)
🔊 Encoded: 112KB WAV → 8KB Opus
📡 Sending to server...

[parakeet/parakeet-tdt-0.6b-v3, lang=es, 3.6s audio, 413ms processing]
🌐 Translating to English...
Hello, how are you? How's it going?
```

## Translation

The `-translate` flag enables post-transcription translation via [Ollama](https://ollama.com/). The transcript is sent to an Ollama LLM model which returns the translation using structured output (JSON schema) for reliable parsing.

**Requirements:**
- An Ollama server running with a pulled model
- The model should support structured output (most modern models do)

**Configuration:**

| Source | Priority | Description |
|---|---|---|
| `-ollama-host` flag | Highest | Explicit host URL |
| `OLLAMA_HOST` env | Default | Standard Ollama environment variable |
| `http://localhost:11434` | Fallback | Default Ollama address |

The host is normalized automatically — bare hostnames like `myhost` become `http://myhost:11434`.

## Audio format

| Property | Value |
|---|---|
| Sample rate | 16,000 Hz |
| Channels | 1 (mono) |
| Encoding (capture) | float32 PCM |
| Encoding (transfer) | Opus, 32kbps VoIP mode |
| Encoding (backup) | 16-bit PCM WAV |

The Opus encoding reduces transfer size by ~95% compared to WAV (e.g., 162KB → 10KB for a 5-second recording), making it practical for long recordings over slow connections.
