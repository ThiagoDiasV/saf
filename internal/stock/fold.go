package stock

import (
	"strings"
	"unicode"
)

var portugueseAccents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n",
)

// fold makes the key two spellings of the same thing share: "Dipirona Sódica 500 mg" and
// "dipirona sodica 500MG" both become "dipironasodica500mg". Case, accents and spaces don't count.
func fold(s string) string {
	s = portugueseAccents.Replace(strings.ToLower(s))
	return strings.Map(func(r rune) rune {
		// Spaces go, and so do combining accents, which some keyboards send as separate characters.
		if unicode.IsSpace(r) || unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, s)
}

// clean trims s and collapses inner runs of spaces, the way a person means what they typed.
func clean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
