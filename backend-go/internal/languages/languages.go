// Package languages maintains the allowlist of translation language codes used
// across the API, routing planner and workers.
//
// IMPORTANT — VERIFY AGAINST PROVIDER DOCS: the codes below MUST be validated
// against the *current* Palabra Speech-to-Speech supported-language matrix at
// deploy time. This registry is a product-level safety net (it stops obviously
// unsupported codes early); it is NOT proof that Palabra can translate a given
// pair. Unsupported-but-allowlisted codes surface as Palabra session errors.
// Operators can extend the allowlist without a code change via the
// EXTRA_LANGUAGES environment variable (see config) by calling ConfigureExtra.
package languages

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Language describes a single supported translation language.
type Language struct {
	// Code is a BCP-47-ish primary subtag, e.g. "en", "ta", "ja", "hi".
	Code string `json:"code"`
	// Name is the English display name, e.g. "Tamil".
	Name string `json:"name"`
	// NativeName is the endonym, e.g. "தமிழ்".
	NativeName string `json:"native_name"`
}

var (
	mu       sync.RWMutex
	registry = map[string]Language{}
)

func init() {
	seed := []Language{
		{Code: "en", Name: "English", NativeName: "English"},
		{Code: "ta", Name: "Tamil", NativeName: "தமிழ்"},
		{Code: "ja", Name: "Japanese", NativeName: "日本語"},
		{Code: "hi", Name: "Hindi", NativeName: "हिन्दी"},
		{Code: "es", Name: "Spanish", NativeName: "Español"},
		{Code: "fr", Name: "French", NativeName: "Français"},
		{Code: "de", Name: "German", NativeName: "Deutsch"},
		{Code: "pt", Name: "Portuguese", NativeName: "Português"},
		{Code: "zh", Name: "Chinese", NativeName: "中文"},
		{Code: "ko", Name: "Korean", NativeName: "한국어"},
		{Code: "ar", Name: "Arabic", NativeName: "العربية"},
		{Code: "ru", Name: "Russian", NativeName: "Русский"},
		{Code: "it", Name: "Italian", NativeName: "Italiano"},
		{Code: "tr", Name: "Turkish", NativeName: "Türkçe"},
		{Code: "vi", Name: "Vietnamese", NativeName: "Tiếng Việt"},
		{Code: "th", Name: "Thai", NativeName: "ไทย"},
		{Code: "id", Name: "Indonesian", NativeName: "Bahasa Indonesia"},
		{Code: "mr", Name: "Marathi", NativeName: "मराठी"},
		{Code: "te", Name: "Telugu", NativeName: "తెలుగు"},
		{Code: "bn", Name: "Bengali", NativeName: "বাংলা"},
		{Code: "ur", Name: "Urdu", NativeName: "اردو"},
		{Code: "ml", Name: "Malayalam", NativeName: "മലയാളം"},
		{Code: "kn", Name: "Kannada", NativeName: "ಕನ್ನಡ"},
		{Code: "gu", Name: "Gujarati", NativeName: "ગુજરાતી"},
		{Code: "pa", Name: "Punjabi", NativeName: "ਪੰਜਾਬੀ"},
		{Code: "pl", Name: "Polish", NativeName: "Polski"},
		{Code: "nl", Name: "Dutch", NativeName: "Nederlands"},
		{Code: "sv", Name: "Swedish", NativeName: "Svenska"},
	}
	for _, l := range seed {
		registry[l.Code] = l
	}
}

// IsValid reports whether code is in the allowlist after normalization.
func IsValid(code string) bool {
	norm, err := Normalize(code)
	if err != nil {
		return false
	}
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[norm]
	return ok
}

// Normalize trims whitespace, lowercases and strips any region/script suffix
// ("en-US" -> "en", "ta_IN" -> "ta"). The primary subtag is what Palabra S2S
// session parameters are expected to use — VERIFY the exact expected form
// against current provider docs before changing this.
func Normalize(code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("language code is empty")
	}
	code = strings.ToLower(code)
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	if code == "" {
		return "", fmt.Errorf("language code %q has no primary subtag", code)
	}
	return code, nil
}

// List returns every allowlisted language sorted by code. Safe for concurrent
// use; the returned slice is a copy.
func List() []Language {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Language, 0, len(registry))
	for _, l := range registry {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Register adds (or replaces) a language in the allowlist. It is intended for
// the EXTRA_LANGUAGES deployment hook and for tests; it is safe for concurrent
// use.
func Register(code, name, nativeName string) error {
	norm, err := Normalize(code)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		name = strings.ToUpper(norm)
	}
	mu.Lock()
	defer mu.Unlock()
	registry[norm] = Language{Code: norm, Name: name, NativeName: nativeName}
	return nil
}

// ConfigureExtra extends the allowlist from a comma-separated list of codes,
// e.g. "fil, eu, ca". Each code is registered with a generated display name
// (uppercase code) unless a nicer name is supplied later via Register. Codes
// already in the allowlist are left untouched. Used at startup from
// config.Config.ExtraLanguages.
func ConfigureExtra(csv string) error {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	for _, part := range strings.Split(csv, ",") {
		code := strings.TrimSpace(part)
		if code == "" {
			continue
		}
		mu.RLock()
		_, exists := registry[code]
		mu.RUnlock()
		if exists {
			continue
		}
		if err := Register(code, strings.ToUpper(code), strings.ToUpper(code)); err != nil {
			return fmt.Errorf("extra language %q: %w", code, err)
		}
	}
	return nil
}
