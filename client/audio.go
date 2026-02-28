package client

// NormalizeAudio scales samples so the peak amplitude reaches 0.9.
// Returns the detected peak and the gain factor applied.
// If the peak is below 0.001, no scaling is applied and gain is 1.0.
func NormalizeAudio(samples []float32) (peak, gain float32) {
	for _, s := range samples {
		if s > peak {
			peak = s
		} else if -s > peak {
			peak = -s
		}
	}
	if peak < 0.001 {
		return peak, 1.0
	}
	gain = 0.9 / peak
	for i := range samples {
		samples[i] *= gain
	}
	return peak, gain
}
