package hxchat

import (
	"context"
	_ "embed"
	"io"
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"

	v2 "github.com/joyboydata/kit/hxkit/v2"
)

//go:embed element.js
var elementJS []byte

//go:embed element.css
var elementCSS []byte

// Asset URLs and the resource path serving them. One fixed path: the
// bytes are identical for every Chat, so all instances share it.
const (
	assetPath = "/hxchat/:file"
	assetJS   = "/hxchat/element.js"
	assetCSS  = "/hxchat/element.css"
)

// serveAsset serves the embedded element and stylesheet. Handlers
// pre-set Content-Type (Echo keeps a preset over its HTML default) and
// immutable Cache-Control: the bytes are version-locked by the Go
// module, so a far-future cache is safe.
func serveAsset(c echo.Context) (templ.Component, error) {
	switch c.Param("file") {
	case "element.js":
		c.Response().Header().Set("Content-Type", "application/javascript")
		c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return bytesComponent(elementJS), nil
	case "element.css":
		c.Response().Header().Set("Content-Type", "text/css")
		c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return bytesComponent(elementCSS), nil
	default:
		return nil, v2.E(http.StatusNotFound, "unknown asset")
	}
}

// bytesComponent renders raw bytes verbatim (scripts, stylesheets).
func bytesComponent(b []byte) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := w.Write(b)
		return err
	})
}
