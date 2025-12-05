package big

import (
	"math/rand"
	"testing"
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


// Test for the modularInverseModPowerOfTwo function.
//
// We verify that it gives the same result as the (general) modInverse function for computations modulu 2**n with
// 1 <= n <= 1024.
func TestInverseModPowerOfTwo(t *testing.T) {
	rnd := rand.New(rand.NewSource(99))
	stk := getStack()
	defer stk.free()
	for n := 1; n <= 1024; n++ {
		powerOfTwo := nat(nil).lsh(nat{1}, uint(n))
		xs := []nat{nat{1}, nat{3}, nat{1, 1}, nat{0x12345}, nat{0x12345, 12345}}
		randomX := nat(nil).random(rnd, powerOfTwo, n+1)
		randomX.setBit(randomX, 0, 1) // inverses of x mod power of two only makes sense for odd x.
		xs = append(xs, randomX)
		randomXSquared := nat(nil).sqr(stk, randomX)
		xs = append(xs, randomXSquared)
		for _, x := range xs {
			// Note that we assume result1 to be correct. But we check it, just in case.
			result1 := nat(nil).make(n/_W + 1)
			result1 = result1.modInverse(x, powerOfTwo)
			check := nat(nil).mul(stk, result1, x)
			check = check.trunc(check, uint(n))
			if check.cmp(nat{1}) != 0 {
				t.Fatalf("Error computing modular inverse of %v modulo 2**%v. Result was %v", x, n, result1)
			}
			result2 := nat(nil).modularInverseModPowerOfTwo(stk, x, uint(n))
			if result1.cmp(result2) != 0 {
				t.Fatalf("Error computing modular inverse of %v modulo 2**%v, result should be %v, but was %v", x, n, result1, result2)
			}
		}
	}
}
