package big

import (
	"math/rand"
	"testing"
)

// Test for the modularInverseModPowerOfTwo function.
//
// We verify that it gives the same result as the (general) modInverse function for computations modulo 2**n with
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
