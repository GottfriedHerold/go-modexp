package big

import "math/rand"

// createRandomInstance creates a random nat with exactly the prescribed bitLength and number of trailing zero bits.
//
// If bitLength is 0, it returns zero. Otherwise, the returned number z has no leading zeroes,
// meaning 2**(bitLength-1) <= z < 2**bitLength.
//
// The returned number will have exactly trailingZeros many trailing 0 bits, followed by a 1 bit.
// If trailingZeros is >= bitLength, it is capped to the maximum meaningful value bitLength-1.
// A negative value for trailingZeros is understood to mean no restriction on trailing zeros and the number is chosen uniformly
// in the interval.
//
// The purpose of this function is to create test cases for benchmarks and differential tests for moduluar exponentiation algorithms.
// It does not need to be fast or have extremely high quality randomness
func createRandomInstance(rand *rand.Rand, bitLength int, trailingZeros int) nat {
	if bitLength < 0 {
		panic("big: called createRandomInstance with negative bitLength")
	}
	if bitLength == 0 {
		z := make(nat, 0, 4)
		return z // we return a 0-length (rather than nil) slice.
	}
	if trailingZeros >= bitLength {
		trailingZeros = int(bitLength) - 1
	}
	powerOf2 := nat{}.setBit(nat{}, uint(bitLength-1), 1) // 2**(bitLength-1)
	tail := nat{}.random(rand, powerOf2, bitLength)
	z := powerOf2.add(powerOf2, tail)

	if trailingZeros >= 0 {
		// set last trailinzZeros bits to 0 ...
		for i := uint(0); i < uint(trailingZeros); i++ {
			z = z.setBit(z, i, 0)
		}
		// ... followed by a 1.
		z = z.setBit(z, uint(trailingZeros), 1)
	}
	return z.norm()
}
