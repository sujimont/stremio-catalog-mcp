package syncer

import "testing"

func TestIsForeign(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Multi-audio & regional Indian content - MUST be preserved (expected false)
		{"Indian Multi Audio Movie", "Devara (2024) [Hindi + Tamil + Telugu + Malayalam] Multi Audio", false},
		{"Indian Dual Audio Movie", "Amaran (2024) Telugu + Tamil Dual Audio", false},
		{"Regional Indian Serial", "Jai Mahendran [Malayalam + Tamil + Telugu + Hindi] Multi Audio", false},
		{"Hotstar Indian Series", "Aakhri Sach", false},
		{"Indian Night Manager", "The Night Manager", false},
		{"Indian Drama", "The Dark Heaven (2024)", false},
		{"Telugu Movie", "SI Yugandhar (2026) Telugu HDRip", false},
		{"Indian Series Gullak", "Gullak", false},
		{"Indian Series Undekhi", "Undekhi", false},
		{"Indian Series Athidhi", "Athidhi", false},
		{"Indian Reality Show", "Celebrity MasterChef India", false},
		{"Indian Reality Tamil", "Celebrity Master Chef - Tamil", false},

		// Foreign dubs - MUST be excluded (expected true)
		{"Foreign Cartoon", "SpongeBob SquarePants", true},
		{"Foreign Franchise LOTR", "The Lord of the Rings: The Rings of Power", true},
		{"Anime Naruto", "Naruto Shippūden", true},
		{"Anime Solo Leveling", "Solo Leveling", true},
		{"Foreign Series Lupin", "Lupin", true},
		{"Foreign Series Cross", "Cross", true},
		{"Foreign Series The Flash", "The Flash", true},
		{"Foreign Series Fallout", "Fallout", true},
		{"Foreign Dubbed Tag", "Gladiator II Hollywood Dubbed", true},
		{"English Dubbed Tag", "Attack on Titan English Dubbed", true},
		{"Foreign Series Days", "Days", true},
		{"Foreign Series Youth", "Youth", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isForeign(tc.input)
			if got != tc.expected {
				t.Errorf("isForeign(%q) = %v; want %v", tc.input, got, tc.expected)
			}
		})
	}
}
