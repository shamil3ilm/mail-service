// Package htmlsan sanitises HTML for safe rendering in a sandboxed iframe.
//
// This is an allowlist sanitiser — everything not explicitly permitted is
// dropped. The goal is to render email bodies in a way that preserves
// formatting and links without allowing any script execution, resource
// loading, or covert data exfiltration.
//
// What survives:
//   - Structural + text tags (p, div, span, br, hr, headings, lists,
//     blockquote, pre, code, table family, b/i/u/strong/em)
//   - <a> with an http/https/mailto href
//   - <img> with an http/https/data:image/* src (see BlockRemoteImages)
//
// What is stripped:
//   - <script>, <iframe>, <object>, <embed>, <form>, <input>, <button>,
//     <link>, <style>, <meta>, <base>, <svg>, <math>
//   - Any on* event-handler attribute
//   - javascript:, vbscript:, file:, and unknown-scheme URLs
//   - style attributes (allowed inline via a strict property allowlist —
//     see stripStyleUnsafe below)
//
// Uses golang.org/x/net/html — already an indirect dep of enmime, so no
// new dependency was fetched.

package htmlsan

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Options controls the sanitiser.
type Options struct {
	// BlockRemoteImages replaces <img src="http..."> with a placeholder
	// (a 1x1 transparent PNG). Blocking remote images is the default in
	// Gmail — prevents tracking pixels from opening on view.
	BlockRemoteImages bool
}

// Sanitize returns HTML safe to embed in an iframe via srcdoc.
// A parse error returns an empty string rather than an error: callers who
// receive "" can fall back to plaintext rendering.
func Sanitize(dirty string, opts Options) string {
	if strings.TrimSpace(dirty) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(dirty))
	if err != nil {
		return ""
	}
	walk(doc, opts)

	var buf bytes.Buffer
	// Serialise only the <body> children to avoid a wrapping <html><head>
	// spilling into our iframe content — those tags aren't harmful but
	// they mess with the iframe's own CSS scoping.
	if body := findBody(doc); body != nil {
		for c := body.FirstChild; c != nil; c = c.NextSibling {
			_ = html.Render(&buf, c)
		}
	} else {
		_ = html.Render(&buf, doc)
	}
	return buf.String()
}

// blockedTags are removed along with their entire subtree.
var blockedTags = map[atom.Atom]struct{}{
	atom.Script:   {},
	atom.Iframe:   {},
	atom.Object:   {},
	atom.Embed:    {},
	atom.Form:     {},
	atom.Input:    {},
	atom.Button:   {},
	atom.Textarea: {},
	atom.Select:   {},
	atom.Link:     {},
	atom.Style:    {},
	atom.Meta:     {},
	atom.Base:     {},
	atom.Svg:      {},
	atom.Math:     {},
}

// allowedTags is the set of tags we render. Anything else gets unwrapped
// (children survive, wrapping tag is dropped).
var allowedTags = map[atom.Atom]struct{}{
	atom.A: {}, atom.Abbr: {}, atom.Address: {}, atom.Article: {},
	atom.B: {}, atom.Blockquote: {}, atom.Body: {}, atom.Br: {},
	atom.Caption: {}, atom.Cite: {}, atom.Code: {}, atom.Col: {}, atom.Colgroup: {},
	atom.Dd: {}, atom.Del: {}, atom.Details: {}, atom.Dfn: {}, atom.Div: {}, atom.Dl: {}, atom.Dt: {},
	atom.Em: {}, atom.Figcaption: {}, atom.Figure: {}, atom.Footer: {},
	atom.H1: {}, atom.H2: {}, atom.H3: {}, atom.H4: {}, atom.H5: {}, atom.H6: {},
	atom.Header: {}, atom.Hr: {}, atom.I: {}, atom.Img: {}, atom.Ins: {}, atom.Kbd: {},
	atom.Li: {}, atom.Main: {}, atom.Mark: {}, atom.Nav: {}, atom.Ol: {},
	atom.P: {}, atom.Pre: {}, atom.Q: {}, atom.S: {}, atom.Samp: {}, atom.Section: {},
	atom.Small: {}, atom.Span: {}, atom.Strong: {}, atom.Sub: {}, atom.Summary: {},
	atom.Sup: {}, atom.Table: {}, atom.Tbody: {}, atom.Td: {}, atom.Tfoot: {},
	atom.Th: {}, atom.Thead: {}, atom.Time: {}, atom.Tr: {}, atom.U: {}, atom.Ul: {},
	atom.Var: {}, atom.Wbr: {},
}

// allowedURLSchemes are the URL schemes we let through on href/src.
var allowedURLSchemes = map[string]bool{
	"http":   true,
	"https":  true,
	"mailto": true,
	"tel":    true,
}

// walk recursively sanitises the DOM in place.
//
// Order matters: for non-allowlisted (but not blocklisted) elements we
// must recurse into their children BEFORE unwrapping the wrapper. If we
// unwrapped first, the promoted children would live at a level our
// current iteration already passed — they'd escape sanitisation entirely.
// This bug bit the root <html> wrapper: <body> and everything inside it
// was never inspected. Recursing first fixes every symptom downstream.
func walk(n *html.Node, opts Options) {
	// Snapshot children first — we may unlink/replace as we iterate.
	children := make([]*html.Node, 0, 4)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		children = append(children, c)
	}
	for _, c := range children {
		if c.Type == html.CommentNode {
			n.RemoveChild(c)
			continue
		}
		if c.Type != html.ElementNode {
			continue
		}
		if _, blocked := blockedTags[c.DataAtom]; blocked {
			n.RemoveChild(c) // drops the whole subtree
			continue
		}

		// Recurse into children FIRST, then decide what to do with the
		// wrapper. Ensures the algorithm visits every descendant regardless
		// of whether the wrapper survives.
		walk(c, opts)

		if _, allowed := allowedTags[c.DataAtom]; !allowed {
			unwrap(c)
			continue
		}
		filterAttrs(c, opts)
	}
}

// unwrap replaces node with its children in the parent.
func unwrap(n *html.Node) {
	parent := n.Parent
	if parent == nil {
		return
	}
	kids := make([]*html.Node, 0, 4)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		kids = append(kids, c)
	}
	for _, k := range kids {
		n.RemoveChild(k)
		parent.InsertBefore(k, n)
	}
	parent.RemoveChild(n)
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == atom.Body {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

// filterAttrs walks an element's attributes and drops any that are unsafe.
// Also rewrites <a href> to add target=_blank + rel=noopener so a click
// doesn't hijack our page.
func filterAttrs(n *html.Node, opts Options) {
	safe := n.Attr[:0]
	hasHref := false
	for _, a := range n.Attr {
		name := strings.ToLower(a.Key)

		// Drop event handlers.
		if strings.HasPrefix(name, "on") {
			continue
		}

		switch name {
		case "href":
			if u := sanitiseURL(a.Val, false); u != "" {
				safe = append(safe, html.Attribute{Key: "href", Val: u})
				hasHref = true
			}
		case "src":
			if opts.BlockRemoteImages && strings.HasPrefix(strings.ToLower(a.Val), "http") {
				// 1x1 transparent PNG data URL — visually harmless placeholder.
				safe = append(safe, html.Attribute{Key: "src", Val: transparentPixelURL})
				safe = append(safe, html.Attribute{Key: "data-blocked-src", Val: a.Val})
			} else if u := sanitiseURL(a.Val, true); u != "" {
				safe = append(safe, html.Attribute{Key: "src", Val: u})
			}
		case "alt", "title", "colspan", "rowspan", "width", "height",
			"align", "valign", "border", "cellpadding", "cellspacing",
			"lang", "dir":
			safe = append(safe, a)
		case "style":
			if s := stripStyleUnsafe(a.Val); s != "" {
				safe = append(safe, html.Attribute{Key: "style", Val: s})
			}
		case "class", "id":
			// Drop — email clients that set these usually collide with
			// the surrounding page's CSS.
		}
	}
	n.Attr = safe

	// External links: open in new tab, without leaking window.opener.
	if n.DataAtom == atom.A && hasHref {
		n.Attr = append(n.Attr,
			html.Attribute{Key: "target", Val: "_blank"},
			html.Attribute{Key: "rel", Val: "noopener noreferrer"},
		)
	}
}

// sanitiseURL returns "" if the URL is unsafe. `isImage` allows data:image/*
// (used for inline images encoded in the message).
func sanitiseURL(raw string, isImage bool) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	low := strings.ToLower(u)
	// Reject known-dangerous schemes even when obfuscated with whitespace.
	for _, bad := range []string{"javascript:", "vbscript:", "file:", "livescript:"} {
		if strings.HasPrefix(low, bad) {
			return ""
		}
	}
	// Relative URLs, anchors, and query-only strings are fine — no scheme.
	i := strings.IndexByte(low, ':')
	if i < 0 || strings.HasPrefix(low, "#") || strings.HasPrefix(low, "/") ||
		strings.HasPrefix(low, "?") {
		return u
	}
	scheme := low[:i]
	if isImage && scheme == "data" && strings.HasPrefix(low, "data:image/") {
		return u
	}
	if !allowedURLSchemes[scheme] {
		return ""
	}
	return u
}

// stylePropertyRE matches "prop: value" pairs where prop is one of an
// allowlist. Values can't contain expressions or url() calls.
var stylePropertyRE = regexp.MustCompile(`(?i)^(color|background-color|font-size|font-weight|font-style|font-family|text-align|text-decoration|padding|padding-top|padding-right|padding-bottom|padding-left|margin|margin-top|margin-right|margin-bottom|margin-left|border|border-color|border-width|border-style|border-radius|line-height|width|height|max-width|min-width|display)$`)

// stripStyleUnsafe returns a filtered inline style string. Anything that
// looks like url(), expression(), or contains @ is dropped as a whole.
func stripStyleUnsafe(raw string) string {
	if raw == "" {
		return ""
	}
	low := strings.ToLower(raw)
	if strings.Contains(low, "url(") || strings.Contains(low, "expression(") ||
		strings.Contains(low, "@import") || strings.Contains(low, "javascript") {
		return ""
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ";") {
		colon := strings.IndexByte(part, ':')
		if colon < 0 {
			continue
		}
		prop := strings.TrimSpace(part[:colon])
		val := strings.TrimSpace(part[colon+1:])
		if val == "" || !stylePropertyRE.MatchString(prop) {
			continue
		}
		out = append(out, prop+": "+val)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "; ")
}

// transparentPixelURL is a 1x1 fully-transparent PNG (68 bytes decoded)
// used as the placeholder when BlockRemoteImages is enabled.
const transparentPixelURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
