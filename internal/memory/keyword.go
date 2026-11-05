package memory

import (
	"strings"
	"unicode"
)

// keywordStopwords are function words whose presence says nothing about
// which memory a query is looking for. Matching on them made a row look
// related to any query that used "the" or "this" (yaah-shq review).
var keywordStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "been": true, "but": true, "by": true, "did": true, "do": true,
	"does": true, "for": true, "from": true, "had": true, "has": true,
	"have": true, "he": true, "her": true, "him": true, "his": true, "how": true,
	"i": true, "if": true, "in": true, "into": true, "is": true, "it": true,
	"its": true, "me": true, "my": true, "no": true, "nor": true, "not": true,
	"of": true, "on": true, "or": true, "our": true, "she": true, "so": true,
	"than": true, "that": true, "the": true, "their": true, "them": true,
	"then": true, "there": true, "these": true, "they": true, "this": true,
	"those": true, "to": true, "too": true, "us": true, "was": true, "we": true,
	"were": true, "what": true, "when": true, "where": true, "which": true,
	"who": true, "why": true, "will": true, "with": true, "you": true, "your": true,
}

// wordTokens splits s into lowercase alphanumeric runs. Unicode letters are
// kept so non-Latin text (the store's Korean examples among them) tokenizes
// into words rather than disappearing.
func wordTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// keywordTokens extracts the searchable words of a query: word-split,
// stopword-free, with single characters dropped (they match everything).
func keywordTokens(query string) []string {
	var out []string
	for _, t := range wordTokens(query) {
		if len(t) < 2 || keywordStopwords[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// keywordScore scores text by the fraction of query tokens it contains,
// scaled into (0, 1]. Matching is whole-word: the earlier substring check
// called "the" a hit inside "weather" and "art" a hit inside "partial",
// inflating unrelated rows. It reports false when no token matches, so a
// row that cannot be compared semantically and shares no word with the
// query is left out rather than padding the result list.
func keywordScore(text string, tokens []string) (float32, bool) {
	if len(tokens) == 0 {
		return 0, false
	}
	words := make(map[string]bool)
	for _, w := range wordTokens(text) {
		words[w] = true
	}
	hits := 0
	for _, t := range tokens {
		if words[t] {
			hits++
		}
	}
	if hits == 0 {
		return 0, false
	}
	return float32(hits) / float32(len(tokens)), true
}
