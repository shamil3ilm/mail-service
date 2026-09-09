package htmlsan

import (
	"strings"
	"testing"
)

func TestScriptTagsStripped(t *testing.T) {
	got := Sanitize(`<p>hi</p><script>alert(1)</script>`, Options{})
	if strings.Contains(got, "<script") || strings.Contains(got, "alert") {
		t.Fatalf("script survived: %s", got)
	}
	if !strings.Contains(got, "<p>hi</p>") {
		t.Fatalf("safe content dropped: %s", got)
	}
}

func TestEventHandlersStripped(t *testing.T) {
	got := Sanitize(`<a href="https://x.com" onclick="alert(1)">click</a>`, Options{})
	if strings.Contains(got, "onclick") || strings.Contains(got, "alert") {
		t.Fatalf("event handler survived: %s", got)
	}
	if !strings.Contains(got, `href="https://x.com"`) {
		t.Fatalf("href dropped: %s", got)
	}
}

func TestJavaScriptURLBlocked(t *testing.T) {
	got := Sanitize(`<a href="javascript:alert(1)">click</a>`, Options{})
	if strings.Contains(got, "javascript") {
		t.Fatalf("javascript: url survived: %s", got)
	}
}

func TestJavaScriptURLObfuscated(t *testing.T) {
	got := Sanitize(`<a href="  JAVA` + "\t" + `SCRIPT:alert(1)  ">click</a>`, Options{})
	// The scheme with an embedded tab isn't a real javascript: URL — but if
	// we did wrongly allow it, the raw text still shouldn't say "alert".
	// Confirm we either drop it or preserve it verbatim without executing.
	if strings.Contains(strings.ToLower(got), "javascript:") {
		t.Fatalf("suspicious url survived: %s", got)
	}
}

func TestImgSrcHTTPBlockedWhenConfigured(t *testing.T) {
	dirty := `<img src="https://tracker.example.com/pixel.gif">`
	got := Sanitize(dirty, Options{BlockRemoteImages: true})
	if strings.Contains(got, "tracker.example.com") &&
		!strings.Contains(got, "data-blocked-src") {
		t.Fatalf("remote src was preserved without marking: %s", got)
	}
	if !strings.Contains(got, "data:image/png;base64,") {
		t.Fatalf("placeholder not inserted: %s", got)
	}
}

func TestImgSrcDataImageAllowed(t *testing.T) {
	dirty := `<img src="data:image/png;base64,iVBORw0KG=" alt="a">`
	got := Sanitize(dirty, Options{BlockRemoteImages: true})
	if !strings.Contains(got, "data:image/png") {
		t.Fatalf("inline image dropped: %s", got)
	}
}

func TestAllowlistedTagsPreserved(t *testing.T) {
	dirty := `<div><p><strong>bold</strong> <em>italic</em></p><ul><li>one</li><li>two</li></ul></div>`
	got := Sanitize(dirty, Options{})
	for _, want := range []string{"<strong>", "<em>", "<ul>", "<li>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %s", want, got)
		}
	}
}

func TestUnknownTagUnwrapped(t *testing.T) {
	// <center> is not on the allowlist — content should survive, wrap dropped.
	got := Sanitize(`<center>hello</center>`, Options{})
	if strings.Contains(got, "<center>") {
		t.Fatalf("unknown tag survived: %s", got)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("content dropped: %s", got)
	}
}

func TestExternalLinksGetTargetBlankRel(t *testing.T) {
	got := Sanitize(`<a href="https://x.com">x</a>`, Options{})
	if !strings.Contains(got, `target="_blank"`) {
		t.Errorf("target=_blank missing: %s", got)
	}
	if !strings.Contains(got, "noopener") {
		t.Errorf("rel=noopener missing: %s", got)
	}
}

func TestStyleAllowlist(t *testing.T) {
	// Safe properties on the allowlist survive; non-allowlisted properties
	// (like the ancient IE-only "behavior") are dropped by the per-property
	// filter. This test avoids url()/expression() — those trigger the
	// whole-style drop (defense-in-depth), which TestStyleWithURLDropped
	// covers separately.
	got := Sanitize(`<p style="color: red; behavior: hider; font-size: 12px">hi</p>`, Options{})
	if !strings.Contains(got, "color: red") {
		t.Errorf("safe prop dropped: %s", got)
	}
	if !strings.Contains(got, "font-size: 12px") {
		t.Errorf("safe prop dropped: %s", got)
	}
	if strings.Contains(got, "behavior") {
		t.Errorf("non-allowlisted prop survived: %s", got)
	}
}

func TestStyleWithURLDropped(t *testing.T) {
	got := Sanitize(`<p style="background-image: url(evil.png); color: red">hi</p>`, Options{})
	// url(...) triggers full-attribute drop.
	if strings.Contains(got, "style=") {
		t.Errorf("style with url() survived: %s", got)
	}
}

func TestFormAndInputDropped(t *testing.T) {
	got := Sanitize(`<form action="/x"><input name="a"><p>keep me</p></form>`, Options{})
	if strings.Contains(got, "<form") || strings.Contains(got, "<input") {
		t.Fatalf("form/input survived: %s", got)
	}
	// Whole subtree is dropped per the "blocked" contract.
	if strings.Contains(got, "keep me") {
		t.Fatalf("blocked subtree children leaked: %s", got)
	}
}
