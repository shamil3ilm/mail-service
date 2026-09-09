package dkim

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// canonicaliseBodyRelaxed implements RFC 6376 §3.4.4 "relaxed" body canonicalisation:
//   - Reduce all sequences of WSP within a line to a single space.
//   - Strip trailing WSP from each line.
//   - Reduce trailing empty lines to a single CRLF (or empty if body is empty).
//
// The input body starts AFTER the header/body CRLF separator.
func canonicaliseBodyRelaxed(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	// Normalise to a slice of lines (split on CRLF, tolerate lone LF too).
	// Trailing empty separator is preserved so we can trim trailing blanks.
	lines := splitLinesKeepCount(body)

	// Per-line: collapse WSP runs, strip trailing WSP.
	for i, ln := range lines {
		lines[i] = collapseAndTrim(ln)
	}

	// Strip trailing empty lines.
	end := len(lines)
	for end > 0 && len(lines[end-1]) == 0 {
		end--
	}
	lines = lines[:end]

	if len(lines) == 0 {
		return nil
	}

	var buf bytes.Buffer
	for _, ln := range lines {
		buf.Write(ln)
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

func splitLinesKeepCount(body []byte) [][]byte {
	// Split on \n so we don't lose empty lines. Trim trailing \r per line.
	raw := bytes.Split(body, []byte("\n"))
	out := make([][]byte, 0, len(raw))
	for _, ln := range raw {
		if len(ln) > 0 && ln[len(ln)-1] == '\r' {
			ln = ln[:len(ln)-1]
		}
		out = append(out, ln)
	}
	// If the body ended with a newline, bytes.Split leaves a trailing empty
	// element which is correct for the "strip trailing blank lines" step.
	return out
}

// collapseAndTrim reduces runs of SP/TAB to a single space and strips
// trailing whitespace. Leading whitespace is preserved (used by
// continuation lines in bodies).
func collapseAndTrim(line []byte) []byte {
	if len(line) == 0 {
		return line
	}
	out := make([]byte, 0, len(line))
	prevSP := false
	for _, b := range line {
		if b == ' ' || b == '\t' {
			if !prevSP {
				out = append(out, ' ')
				prevSP = true
			}
			continue
		}
		out = append(out, b)
		prevSP = false
	}
	// Strip trailing spaces (we may have added one from the last WSP run).
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return out
}

// canonicaliseHeadersForSigning returns the header block prepared per
// RFC 6376 §3.4.2 "relaxed" header canonicalisation, in the order given by
// `signed` (the h= list). Each header appears at most once — if a message
// has multiple headers with the same name (rare, mostly Received:), the
// last occurrence is used per spec.
func canonicaliseHeadersForSigning(headers []byte, signed []string) []byte {
	all := parseHeaderBlock(headers)
	var buf bytes.Buffer
	for _, name := range signed {
		val, ok := lastValueCI(all, name)
		if !ok {
			continue
		}
		buf.WriteString(strings.ToLower(name))
		buf.WriteByte(':')
		buf.WriteString(canonicaliseHeaderValueRelaxed(val))
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

// canonicaliseDKIMHeader canonicalises the DKIM-Signature header itself
// for inclusion in the signing input. The b= tag value must be empty at
// this point (its value is what we're computing).
//
// The trailing CRLF that concludes the header block is deliberately NOT
// appended — RFC 6376 §3.7 states that when the DKIM-Signature header is
// hashed with the other signed headers, its terminating CRLF is stripped.
func canonicaliseDKIMHeader(header string) []byte {
	// header is like: "DKIM-Signature: v=1; a=...; b="
	colon := strings.IndexByte(header, ':')
	if colon < 0 {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(header[:colon]))
	value := header[colon+1:]
	// Strip any trailing CRLF the builder may have added.
	value = strings.TrimRight(value, "\r\n")

	return []byte(name + ":" + canonicaliseHeaderValueRelaxed(value))
}

// canonicaliseHeaderValueRelaxed:
//   - Unfold (remove any CRLF followed by WSP).
//   - Reduce runs of WSP to single SP.
//   - Strip leading and trailing WSP.
func canonicaliseHeaderValueRelaxed(v string) string {
	// Unfold.
	v = strings.ReplaceAll(v, "\r\n ", " ")
	v = strings.ReplaceAll(v, "\r\n\t", " ")
	// Collapse WSP.
	var b strings.Builder
	prevSP := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == ' ' || c == '\t' {
			if !prevSP {
				b.WriteByte(' ')
				prevSP = true
			}
			continue
		}
		b.WriteByte(c)
		prevSP = false
	}
	return strings.TrimSpace(b.String())
}

// parseHeaderBlock splits a header block into ordered (name, value) pairs,
// honouring RFC 5322 folded continuation lines (leading WSP).
type headerLine struct{ Name, Value string }

func parseHeaderBlock(block []byte) []headerLine {
	lines := bytes.Split(block, []byte("\r\n"))
	var out []headerLine
	var cur *headerLine
	for _, ln := range lines {
		if len(ln) == 0 {
			continue
		}
		if ln[0] == ' ' || ln[0] == '\t' {
			// Continuation of the previous header.
			if cur == nil {
				continue
			}
			cur.Value += "\r\n" + string(ln)
			continue
		}
		if cur != nil {
			out = append(out, *cur)
		}
		i := bytes.IndexByte(ln, ':')
		if i < 0 {
			cur = nil
			continue
		}
		cur = &headerLine{
			Name:  string(ln[:i]),
			Value: string(ln[i+1:]),
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// lastValueCI returns the value of the last header matching name (case-
// insensitive), matching RFC 6376's "last occurrence wins" rule.
func lastValueCI(lines []headerLine, name string) (string, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.EqualFold(lines[i].Name, name) {
			return lines[i].Value, true
		}
	}
	return "", false
}

// collectPresentHeaders returns wanted headers filtered to those present in
// the block, preserving wanted's order. Used to build the h= tag.
func collectPresentHeaders(block []byte, wanted []string) []string {
	all := parseHeaderBlock(block)
	present := make(map[string]struct{}, len(all))
	for _, h := range all {
		present[strings.ToLower(h.Name)] = struct{}{}
	}
	out := make([]string, 0, len(wanted))
	for _, w := range wanted {
		if _, ok := present[strings.ToLower(w)]; ok {
			out = append(out, w)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return false }) // preserve
	return out
}

// dkimTags is the flat set of tags we emit. Order matches the canonical
// example in RFC 6376 §3.5.
type dkimTags struct {
	Version     string
	Algorithm   string
	Canon       string
	Domain      string
	Selector    string
	Timestamp   int64
	Headers     []string
	BodyHashB64 string
	Signature   string
}

func buildDKIMHeader(t dkimTags) string {
	return fmt.Sprintf(
		"DKIM-Signature: v=%s; a=%s; c=%s; d=%s; s=%s; t=%d;\r\n"+
			"\th=%s;\r\n"+
			"\tbh=%s;\r\n"+
			"\tb=%s",
		t.Version, t.Algorithm, t.Canon, t.Domain, t.Selector, t.Timestamp,
		strings.Join(t.Headers, ":"),
		t.BodyHashB64,
		t.Signature,
	)
}
