# lunartlk

On-device speech-to-text. Record from your microphone, get a transcript back. Works in English and Spanish.

Powered by [Moonshine](https://github.com/moonshine-ai/moonshine) — runs locally, no cloud, no API keys.

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

The server loads both language models and listens on port 9765. On first run it extracts bundled libraries and models to `~/.cache/lunartlk/`.

```bash
# English as default language
./bin/lunartlk-server -lang en

# Custom port
./bin/lunartlk-server -addr :8080
```

### Record and transcribe

```bash
./bin/lunartlk-client
```

Speak, then press Ctrl+C. The recording is sent to the server and the transcript is printed.

```bash
# Transcribe in English
./bin/lunartlk-client -lang en

# Remote server
./bin/lunartlk-client -server http://myserver:9765

# Copy result to Wayland clipboard
./bin/lunartlk-client -clipboard
```

### Transcribe a file

```bash
curl -F 'audio=@recording.wav' http://localhost:9765/transcribe
curl -F 'audio=@recording.wav' 'http://localhost:9765/transcribe?lang=en'
```

### Check your setup

```bash
./bin/lunartlk-client -doctor
./bin/lunartlk-server -doctor
```

## How it works

The **client** records audio from your microphone, encodes it as Opus in real-time (~95% smaller than WAV), and POSTs it to the server. A backup WAV is saved to `/tmp/` in case the server is unreachable.

The **server** is a single self-extracting file that bundles the Go binary, Moonshine C library, ONNX Runtime, and model weights. It decodes the audio, runs speech-to-text inference on CPU, and returns a JSON transcript with timestamps.

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
