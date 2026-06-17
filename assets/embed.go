package assets

import (
	"embed"
)

//go:embed i18n/*.json
var I18nFS embed.FS
