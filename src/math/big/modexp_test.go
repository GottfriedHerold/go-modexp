package big

import (
	"math/rand"
)

// createRandomInstance creates a random nat with exactly the prescribed bitLength and (optionally) number of trailing zero bits.
//
// If bitLength is 0, it returns zero. Otherwise, the returned number z has no leading zeroes,
// meaning 2**(bitLength-1) <= z < 2**bitLength.
//
// trailingZeros is an optional single uint. If provided, the returned number will have exactly trailingZeros many trailing 0 bits,
// followed by a 1 bit.
// If trailingZeros is >= bitLength, it is capped to the maximum meaningful value bitLength-1.
//
// The purpose of this function is to create test cases for benchmarks and differential tests for moduluar exponentiation algorithms.
// It does not need to be fast or have extremely high quality randomness
func createRandomInstance(rand *rand.Rand, bitLength int, trailingZeros ...uint) nat {
	if bitLength < 0 {
		panic("big: called createRandomInstance with negative bitLength")
	}
	if len(trailingZeros) >= 2 {
		panic("big: called createRandomInstance with >= 2 optional arguments")
	}
	if bitLength == 0 {
		z := make(nat, 0, 4)
		return z // we return a 0-length (rather than nil) slice.
	}
	powerOf2 := nat{}.setBit(nat{}, uint(bitLength-1), 1) // 2**(bitLength-1)
	tail := nat{}.random(rand, powerOf2, bitLength)
	z := powerOf2.add(powerOf2, tail)

	if len(trailingZeros) != 0 {
		actualTrailingZeros := trailingZeros[0]
		if actualTrailingZeros >= uint(bitLength) {
			actualTrailingZeros = uint(bitLength - 1)
		}
		// set last trailinzZeros bits to 0.
		for i := uint(0); i < actualTrailingZeros; i++ {
			z = z.setBit(z, i, 0)
		}
		// and the next (higher) bit to 1.
		z = z.setBit(z, actualTrailingZeros, 1)
	}
	return z.norm()
}
