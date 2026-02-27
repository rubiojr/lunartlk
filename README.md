# lunartlk

On-device speech-to-text. Record from your microphone, get a transcript back.

Two engines:
- **Moonshine** — fast, lightweight (English + Spanish)
- **Parakeet v3** — NVIDIA's best, 25 languages, highest accuracy

Runs locally, no cloud, no API keys. Models download automatically on first use.

## Install

### Prerequisites

**Fedora:**
```bash
sudo dnf install -y gcc gcc-c++ cmake git portaudio-devel \
  pipewire-jack-audio-connection-kit-devel opus-devel opusfile-devel zstd
```

**Debian / Ubuntu / Raspberry Pi:**
```bash
sudo apt install -y build-essential cmake git portaudio19-dev \
  libopus-dev libopusfile-dev zstd
```

Go 1.21+ is also required.

### Build

```bash
git clone <repo-url> lunartlk && cd lunartlk
./scripts/build.sh
```

This clones Moonshine, builds the C library, downloads the English and Spanish models, and produces two binaries in `bin/`.

## Usage

### Start the server

```bash
./bin/lunartlk-server
```

The server listens on port 9765. Models download automatically on first request (lazy loading — only uses RAM for engines you actually call).

```bash
# Use Parakeet as default engine (best accuracy)
./bin/lunartlk-server -engine parakeet

# English default with auth
./bin/lunartlk-server -lang en -token mysecret
```

### Record and transcribe

```bash
./bin/lunartlk-client
```

Speak, then press Ctrl+C. The recording is sent to the server and the transcript is printed.

```bash
# Use Parakeet engine (25 languages)
./bin/lunartlk-client -engine parakeet

# Moonshine English
./bin/lunartlk-client -lang en

# Remote server with auth
./bin/lunartlk-client -server http://myserver:9765 -token mysecret

# Copy result to Wayland clipboard
./bin/lunartlk-client -clipboard
```

### Transcribe a file

```bash
# Default engine
curl -F 'audio=@recording.wav' http://localhost:9765/transcribe

# Parakeet engine
curl -F 'audio=@recording.wav' 'http://localhost:9765/transcribe?engine=parakeet'

# Moonshine English
curl -F 'audio=@recording.wav' 'http://localhost:9765/transcribe?engine=moonshine&lang=en'
```

### Check your setup

```bash
./bin/lunartlk-client -doctor
./bin/lunartlk-server -doctor
```

## How it works

The **client** records audio from your microphone, encodes it as Opus in real-time (~95% smaller than WAV), and POSTs it to the server. A backup WAV is saved to `/tmp/` in case the server is unreachable. Transcripts and audio are saved to `~/.local/share/lunartlk/`.

The **server** bundles shared libraries in a self-extracting wrapper (~40MB). Models download automatically on first use (~200MB for Moonshine, ~640MB for Parakeet). Models are lazy-loaded — only the engine you request uses RAM.

See [docs/client.md](docs/client.md) and [docs/server.md](docs/server.md) for full details.

## Supported platforms

| Platform | Client | Server |
|---|---|---|
| Fedora (x86_64) | ✅ | ✅ |
| Debian/Ubuntu (x86_64) | ✅ | ✅ |
| Raspberry Pi 5 (arm64) | ✅ | ✅ |

## License

The code in this repository is MIT licensed.

The bundled Moonshine models have their own licenses — English models are MIT, other languages use the Moonshine Community License (non-commercial). See [MODELS-LICENSE.md](MODELS-LICENSE.md) for details.
