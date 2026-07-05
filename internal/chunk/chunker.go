// Package chunk splits note content into embedding-sized pieces.
package chunk

import "strings"

const (
	// TargetSize is the size chunks are greedily packed toward.
	TargetSize = 1200
	// MaxSize is the hard per-chunk ceiling; oversized paragraphs are split
	// on sentence boundaries to stay under it.
	MaxSize = 3000
	// OverlapSize caps how much of the previous chunk's trailing paragraph
	// is carried into the next chunk for context continuity.
	OverlapSize = 300
)

// Split chunks text on blank-line paragraph boundaries, greedily packing
// paragraphs up to TargetSize. The last paragraph of each chunk (capped at
// OverlapSize) is repeated at the start of the next chunk.
func Split(text string) []string {
	paragraphs := splitParagraphs(text)
	if len(paragraphs) == 0 {
		return nil
	}

	// Break any paragraph that alone exceeds MaxSize into sentence-packed
	// pieces so the packer below never emits an oversized chunk.
	var units []string
	for _, p := range paragraphs {
		if len(p) <= MaxSize {
			units = append(units, p)
			continue
		}
		units = append(units, splitOversized(p)...)
	}

	var chunks []string
	var current []string
	currentLen := 0

	flush := func() {
		if len(current) == 0 {
			return
		}
		chunks = append(chunks, strings.Join(current, "\n\n"))
		overlap := tail(current[len(current)-1], OverlapSize)
		current = []string{overlap}
		currentLen = len(overlap)
	}

	for _, u := range units {
		joined := currentLen
		if len(current) > 0 {
			joined += 2 // "\n\n"
		}
		joined += len(u)
		if len(current) > 0 && joined > TargetSize {
			flush()
		}
		// Drop the carried overlap when the incoming unit is so large that
		// overlap + unit would breach the hard ceiling.
		if len(current) == 1 && len(chunks) > 0 && currentLen+2+len(u) > MaxSize {
			current = nil
			currentLen = 0
		}
		current = append(current, u)
		currentLen += len(u)
		if len(current) > 1 {
			currentLen += 2
		}
	}
	if len(current) > 0 {
		// Don't emit a chunk that is nothing but carried-over overlap.
		if len(chunks) == 0 || len(current) > 1 || current[0] != tail(chunks[len(chunks)-1], OverlapSize) {
			chunks = append(chunks, strings.Join(current, "\n\n"))
		}
	}
	return chunks
}

func splitParagraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitOversized packs sentences of a too-long paragraph into pieces of at
// most MaxSize, falling back to a hard byte split for pathological input.
func splitOversized(p string) []string {
	sentences := splitSentences(p)
	var out []string
	var cur strings.Builder
	for _, s := range sentences {
		for len(s) > MaxSize {
			if cur.Len() > 0 {
				out = append(out, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
			out = append(out, s[:MaxSize])
			s = s[MaxSize:]
		}
		if cur.Len() > 0 && cur.Len()+1+len(s) > MaxSize {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(s)
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

// splitSentences is a deliberately simple splitter: breaks after ., ! or ?
// followed by whitespace. Good enough for chunking boundaries.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text)-1; i++ {
		if (text[i] == '.' || text[i] == '!' || text[i] == '?') && (text[i+1] == ' ' || text[i+1] == '\n') {
			out = append(out, strings.TrimSpace(text[start:i+1]))
			start = i + 1
		}
	}
	if rest := strings.TrimSpace(text[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

// tail returns the last at-most-n bytes of s, but never splits a word: it
// starts from the first space within the window.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[len(s)-n:]
	if i := strings.IndexByte(cut, ' '); i >= 0 {
		cut = cut[i+1:]
	}
	return cut
}
