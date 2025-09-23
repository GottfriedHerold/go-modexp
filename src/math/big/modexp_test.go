package big

import (
	"math/rand"
	"testing"
)

func TestGenerateMontgomeryParamters(t *testing.T) {
	stk := getStack()
	defer stk.free()

	rnd := rand.New(rand.NewSource(11)) // arbitrarily de-randomized to improve reproducibility
	testModulus := func(stk *stack, m nat) {
		defer stk.restore(stk.save())

		if len(m) == 0 || m[0]&1 == 0 { // m is even, we expect getMontgomeryConstants to panic
			defer func() {
				panicValue := recover()
				if panicValue == nil {
					t.Fatalf("big: getMontgomeryConstants does not panic for (invalid) input m==%v", m)
				}
			}()
			_, _, _ = getMontgomeryConstants(stk, m)
			return
		}

		numWords := len(m)
		k0, RR, one := getMontgomeryConstants(stk, m)
		if -k0*m[0] != 1 {
			t.Fatalf("big: getMontgomeryConstants does not output correct value for k0. Value output was %v for m==%v, m[0]==%v", k0, m, m[0])
		}
		if len(RR) != numWords {
			t.Fatalf("big: getMontgomeryConstants outputs RR==%v of wrong length %v instead of %v for m==%v", RR, len(RR), len(m), m)
		}
		if len(one) != numWords {
			t.Fatalf("big: getMontgomeryConstants outputs one==%v of wrong length %v instead of %v for m==%v", RR, len(one), len(m), m)
		}
		natOne := nat(one).norm()
		natRR := nat(RR).norm()
		natOneExpected := nat(nil).setWord(1)
		if natOne.cmp(natOneExpected) != 0 {
			t.Fatalf("big: getMontgomeryConstants outputs %v instead of 1 for m==%v", natOne, m)
		}
		Pow2 := nat(nil).setWord(1)
		Pow2 = Pow2.lsh(Pow2, _W*2*uint(numWords))
		zz := nat(nil).mul(stk, natRR, Pow2)
		_, zz = nat(nil).div(stk, zz, zz, m)
		_, natOneExpected = nat(nil).div(stk, natOneExpected, natOneExpected, m) // reduce one modulo m. This only matters if m==1.
		if zz.cmp(natOneExpected) != 0 {
			t.Fatalf("big: getMontgomeryConstants does not output inverse of 4**(_W * len(m)). Value output was RR==%v for m==%v", RR, m)
		}
	}
	testModulus(stk, nat(nil).setWord(1))
	testModulus(stk, nat(nil).setWord(0xFF))
	testModulus(stk, nat(nil).setWord(0xFFFF))
	testModulus(stk, nat(nil).setWord(0xFFFFFFFFFF))
	testModulus(stk, nat(nil).setWord(Word(^uint(0))))

	testModulus(stk, nat(nil))
	for i := uint(0); i < 256; i += 32 {
		_, _, m := createBaseModExp(rnd, i, i, 1, 0, 0)
		testModulus(stk, m)
		_, _, m = createBaseModExp(rnd, i, i, 1, 1, 0)
		testModulus(stk, m)
	}
}
