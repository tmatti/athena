package chunk

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitEmpty(t *testing.T) {
	require.Nil(t, Split(""))
	require.Nil(t, Split("   \n\n  \n"))
}

func TestSplitShortTextIsOneChunk(t *testing.T) {
	text := "A short paragraph.\n\nAnd another one."
	chunks := Split(text)
	require.Len(t, chunks, 1)
	require.Equal(t, text, chunks[0])
}

func TestSplitPacksParagraphsToTarget(t *testing.T) {
	para := strings.Repeat("word ", 100) // ~500 chars
	text := strings.TrimSpace(strings.Repeat(para+"\n\n", 6))

	chunks := Split(text)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		require.LessOrEqual(t, len(c), MaxSize)
	}
}

func TestSplitOverlapCarriesTrailingParagraph(t *testing.T) {
	// Two chunks worth of distinct paragraphs; the second chunk must start
	// with (the tail of) the last paragraph of the first.
	p1 := "first " + strings.Repeat("alpha ", 150)  // ~900 chars
	p2 := "second " + strings.Repeat("beta ", 100)  // ~500 chars -> overflows target with p1
	p3 := "third " + strings.Repeat("gamma ", 100)

	chunks := Split(strings.Join([]string{p1, p2, p3}, "\n\n"))
	require.GreaterOrEqual(t, len(chunks), 2)

	first := chunks[0]
	second := chunks[1]
	lastParaOfFirst := first[strings.LastIndex(first, "\n\n")+2:]
	overlap := lastParaOfFirst
	if len(overlap) > OverlapSize {
		overlap = overlap[len(overlap)-OverlapSize:]
		overlap = overlap[strings.IndexByte(overlap, ' ')+1:]
	}
	require.True(t, strings.HasPrefix(second, overlap),
		"second chunk should start with the previous chunk's trailing paragraph")
}

func TestSplitOversizedParagraph(t *testing.T) {
	sentence := "This is a fairly ordinary sentence that keeps going for a while. "
	text := strings.TrimSpace(strings.Repeat(sentence, 80)) // ~5200 chars, one paragraph

	chunks := Split(text)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		require.LessOrEqual(t, len(c), MaxSize)
	}
	// No content lost: every sentence occurrence count survives chunking.
	joined := strings.Join(chunks, " ")
	require.GreaterOrEqual(t, strings.Count(joined, "ordinary sentence"), 80)
}

func TestSplitPathologicalNoSpaces(t *testing.T) {
	text := strings.Repeat("x", 10_000)
	chunks := Split(text)
	require.NotEmpty(t, chunks)
	for _, c := range chunks {
		require.LessOrEqual(t, len(c), MaxSize)
	}
}
