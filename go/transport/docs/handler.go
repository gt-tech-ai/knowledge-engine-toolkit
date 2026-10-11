// Package docs provides reusable HTTP handlers for serving OpenAPI specs
// and Redoc UI documentation pages.
package docs

import (
	"html/template"
	"net/http"
)

// SpecHandler returns an http.HandlerFunc that serves a raw OpenAPI JSON spec.
func SpecHandler(spec []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(spec)
	}
}

// redocHTML is the template of a minimal HTML page that loads Redoc from CDN.
const redocHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>{{.Title}} — API Docs</title>
  <style>body { margin: 0; padding: 0; }</style>
</head>
<body>
  <redoc spec-url="{{.SpecURL}}" hide-hostname></redoc>
  <script src="https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js"></script>
</body>
</html>
`

// redocData holds template variables for the Redoc HTML page.
type redocData struct {
	// Title is the page title shown in the browser tab.
	Title string

	// SpecURL is the URL path where the raw OpenAPI JSON spec is served.
	SpecURL string
}

// RedocHandler returns an http.HandlerFunc that serves a Redoc UI page
// pointing at the given spec URL.
func RedocHandler(specURL, title string) http.HandlerFunc {
	redocTmpl := template.Must(template.New("redoc").Parse(redocHTML))
	data := redocData{
		Title:   title,
		SpecURL: specURL,
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = redocTmpl.Execute(w, data)
	}
}
