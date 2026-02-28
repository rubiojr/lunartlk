package translate

import "context"

// Translator translates text into a target language.
type Translator interface {
	Translate(ctx context.Context, text, toLang string) (string, error)
}
