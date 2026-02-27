# lunartlk

Self-contained speech-to-text toolkit powered by [Moonshine](https://github.com/moonshine-ai/moonshine).

## Quick Start

```bash
# Build everything (checks prereqs, clones moonshine, builds libs, downloads models, builds binaries)
./scripts/build.sh

# Start the server
./bin/lunartlk-server

# In another terminal, record and transcribe
./bin/lunartlk-client
```

## Components

- **lunartlk-server** — Self-extracting HTTP transcription server (bundles libs + models)
- **lunartlk-client** — Mic capture client, sends audio to server for transcription

## Server API

```bash
# Transcribe a WAV file
curl -F 'audio=@recording.wav' http://localhost:9765/transcribe

# Health check
curl http://localhost:9765/health
```

## Build Options

```bash
# Build with a specific model (default: base-es)
MODEL=medium-streaming-en ARCH=5 ./scripts/build.sh

# Available models:
#   tiny-en (arch 0)           - fastest, English
#   base-en (arch 1)           - good, English
#   base-es (arch 1)           - good, Spanish (default)
#   tiny-streaming-en (arch 2) - fast streaming, English
#   base-streaming-en (arch 3) - good streaming, English
#   small-streaming-en (arch 4)- better streaming, English
#   medium-streaming-en (arch 5)- best streaming, English
```

## Prerequisites (Fedora)

```bash
sudo dnf install -y gcc g++ cmake git portaudio-devel pipewire-jack-audio-connection-kit-devel zstd
```

Go 1.21+ required.
