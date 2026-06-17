package main

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/pkg/errors"

	"github.com/naicoi92/mattermost-plugin-task/assets"
)

const (
	defaultLocale = "en"
	i18nDir       = "i18n"
)

// I18n holds translations loaded from embedded JSON files.
type I18n struct {
	messages map[string]map[string]string
}

// NewI18n loads all translation files bundled via //go:embed.
// It returns an error if the embedded files cannot be read or parsed.
func NewI18n() (*I18n, error) {
	files, err := assets.I18nFS.ReadDir(i18nDir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read embedded i18n directory")
	}

	messages := make(map[string]map[string]string)
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		locale := strings.TrimSuffix(file.Name(), ".json")
		data, err := assets.I18nFS.ReadFile(path.Join(i18nDir, file.Name()))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read i18n file %s", file.Name())
		}

		var bundle map[string]string
		if err := json.Unmarshal(data, &bundle); err != nil {
			return nil, errors.Wrapf(err, "failed to parse i18n file %s", file.Name())
		}

		messages[locale] = bundle
	}

	if _, ok := messages[defaultLocale]; !ok {
		return nil, errors.Errorf("default locale %q not found in embedded i18n files", defaultLocale)
	}

	return &I18n{messages: messages}, nil
}

// T returns the translation for the given locale and key.
// If the locale is missing, it falls back to the default locale (en).
// If the key is missing, it returns the key itself.
// Args are interpolated into the translation string using fmt.Sprintf.
func (i *I18n) T(locale, key string, args ...any) string {
	if i == nil {
		return key
	}

	bundle, ok := i.messages[locale]
	if !ok {
		bundle = i.messages[defaultLocale]
	}

	message, ok := bundle[key]
	if !ok {
		message = i.messages[defaultLocale][key]
	}

	if message == "" {
		return key
	}

	if len(args) > 0 {
		return fmt.Sprintf(message, args...)
	}

	return message
}
