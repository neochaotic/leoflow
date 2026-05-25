package setup

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// passwordWords are short, easy-to-spell, unambiguous words for the humanized
// admin password. Kept to 3–6 letters so "word + a few digits" stays within ten
// characters and is trivial to jot down.
var passwordWords = []string{
	"river", "maple", "cloud", "stone", "tiger", "amber", "delta", "comet",
	"otter", "mango", "pearl", "raven", "birch", "coral", "ember", "fjord",
	"glade", "heron", "ivory", "larch", "lunar", "moss", "nova", "olive",
	"opal", "quartz", "reed", "sage", "spark", "tidal", "vine", "wren",
	"zephyr", "aspen", "basil", "cedar", "dune", "fern", "grove", "haze",
}

// GenerateHumanPassword returns a short, human-readable admin password: a random
// word followed by 2–4 random digits, lowercase letters and digits only, at most
// ten characters. It is easy to write down and unambiguous, while still drawn
// from crypto/rand so it is not guessable.
func GenerateHumanPassword() (string, error) {
	word, err := pickWord()
	if err != nil {
		return "", err
	}
	// Leave room for digits within the 10-char budget; use 2..4 digits.
	maxDigits := 10 - len(word)
	if maxDigits > 4 {
		maxDigits = 4
	}
	digits, err := randInt(maxDigits - 1) // 0..(maxDigits-2)
	if err != nil {
		return "", err
	}
	n := digits + 2 // 2..maxDigits
	out := word
	for i := 0; i < n; i++ {
		d, derr := randInt(10)
		if derr != nil {
			return "", derr
		}
		out += fmt.Sprintf("%d", d)
	}
	return out, nil
}

func pickWord() (string, error) {
	i, err := randInt(len(passwordWords))
	if err != nil {
		return "", err
	}
	return passwordWords[i], nil
}

// randInt returns a uniform random int in [0, n) using crypto/rand.
func randInt(n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, fmt.Errorf("reading randomness: %w", err)
	}
	return int(v.Int64()), nil
}
