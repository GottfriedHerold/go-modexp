package big

import (
	"fmt"
	"math/rand"
	"testing"
)

// computeModExpGasSimplfied computes the (simplified) gas cost corresponding to a ModExp-precompile call with a
// base and modulus of a given length (in bytes) and an exponents of a given length in bits.
//
// version must be either "EIP2565" or "EIP7883" to select the corresponding gas pricing function.
//
// Note that this does not 100% match the gas cost specified in the EIP. The latter may differ in case of (a large number of) leading zeros.
// The reason why this simplification is allowed here is that we only use this to benchmark the implementation of nat.expNN and creating a nat will normalize away leading zeros. Also, we don't really care about overflows.
func computeModExpGasSimplified(baseByteLength uint, modulusByteLength uint, exponentBitLenght uint, version string) uint64 {
	if modulusByteLength == 0 {
		panic("called computeModExpGasSimplified with a modulus byte length of 0") // This would correspond to an exponentiation in Z, rather than a modular exponentiation.
	}

	// maxLen := Max{baseByteLength, modulusByteLength}
	var maxLen uint = baseByteLength
	if modulusByteLength > maxLen {
		maxLen = modulusByteLength
	}
	switch version {
	case "EIP2565":
		WordLength := (maxLen + 7) / 8 // number of 64-bit words in max{modulus,base}
		MultComplexity := uint64(WordLength) * uint64(WordLength)
		var iteration_count uint64
		if exponentBitLenght <= 1 {
			iteration_count = 1
		} else {
			iteration_count = uint64(exponentBitLenght - 1) // the case distinction exponentBitLength > 32 from the EIP is not needed due to not considering leading 0s.
		}
		result := (MultComplexity * iteration_count) / 2
		if result < 200 {
			result = 200
		}
		return result

	case "EIP7883":
		WordLength := (maxLen + 7) / 8 // number of 64-bit words in max{modulus,base}
		MultComplexity := uint64(16)
		if maxLen > 32 {
			MultComplexity = 2 * uint64(WordLength) * uint64(WordLength) // strangely discontinuous formula, but that's what the EIP says.
		}
		var iteration_count uint64
		if exponentBitLenght <= 1 {
			iteration_count = 1
		} else if exponentBitLenght <= 256 {
			iteration_count = uint64(exponentBitLenght - 1)
		} else {
			iteration_count = uint64(exponentBitLenght-1) + 8*((uint64(exponentBitLenght)+7)/8-32)
		}
		result := MultComplexity * iteration_count
		if result < 500 {
			result = 500
		}
		return result

	default:
		panic(fmt.Sprintf("math/big/computeModExpGasSimplied: could not recognize version string %v. Valid inputs are \"EIP2565\" and \"EIP7883\"", version))
	}
}

// createBaseModExp creates a random triple (base, exponent, modulus) of nats with the prescribed lengths in bytes resp. bits.
// Modulus will have exactly modulus2adicity many trailing zeros among its 8*modulusByteLength many bits.
//
// Note that this functions guarantees that the byte-length / bit-length is *exactly* the requested amount by setting the appropriate highest bit to 1. Consequently, the length arguments must not be 0.
// If modulus2adicity is >= 8*modulusByteLength, we truncate modulus2adicity to its maximum meaningful value of 8*modulusByteLength-1 instead.
func createBaseModExp(rand *rand.Rand, baseByteLength uint, modulusByteLength uint, exponentBitLength uint, modulus2adicity uint) (base nat, exponent nat, modulus nat) {
	// NOTE: This is not the most efficient way to create those numbers. We do not care.
	if modulus2adicity >= 8*modulusByteLength {
		modulus2adicity = 8*modulusByteLength - 1
	}
	if baseByteLength == 0 {
		panic("baseByteLength set to 0")
	}
	if modulusByteLength == 0 {
		panic("modulusByteLength set to 0")
	}
	if exponentBitLength == 0 {
		panic("exponentBitLength set to 0")
	}
	// create a random number for modulus with *exactly* modulusByteLength bytes with the highest bit set.
	// we also ensure that exactly modulus2adicity least significant bits are 0, followed by a 1.
	modulusPowerOf2 := nat{}.setBit(nat{}, 8*modulusByteLength-1, 1)             // set highest bit to 1
	modulusTail := nat{}.random(rand, modulusPowerOf2, 8*int(modulusByteLength)) // set other bits randomly
	modulus = modulusTail.add(modulusTail, modulusPowerOf2)

	// set modulus2adicity many least significant bits to 0 and the next one to 1.
	for i := uint(0); i < modulus2adicity; i++ {
		modulus = modulus.setBit(modulus, i, 0)
	}
	modulus = modulus.setBit(modulus, modulus2adicity, 1)

	// set base to a random number with exactly baseByteLength many bytes, again with the highest bit forcibly set to 1.
	basePowerOf2 := nat{}.setBit(nat{}, 8*baseByteLength-1, 1)
	baseTail := nat{}.random(rand, basePowerOf2, 8*int(baseByteLength))
	base = baseTail.add(baseTail, basePowerOf2)

	// set exponent to a random number with exactly exponentBitLenght bits (again, msb set to 1)
	// NOTE: Using a random bit-pattern for the exponent is expected to be the worst case for a fixed-window exponentiation.
	// If we use a different exponentiation algorithm, this might no longer be true.
	exponentPowerOf2 := nat{}.setBit(nat{}, exponentBitLength-1, 1)
	exponentTail := nat{}.random(rand, exponentPowerOf2, int(exponentBitLength))
	exponent = exponentPowerOf2.add(exponentPowerOf2, exponentTail)
	return
}

// benchmarkNatExpNN returns a benchmarking function (intendend for use with *testing.B.Run) that runs
// a benchmark on nat.expNN with the given parameters.
//
// Notably, it calls nat.expNN to compute base^exponent modulo modulus, where
// base, exponent resp. modulus have exactly baseByteLength, modulusByteLength resp. exponentBitLength many bytes/bits.
// Note that having exactly the given number of bytes/bits means that the leading byte/bit is guaranteed to be 1.
// modulus2adicty is used to select the number of trailing 0 bits of the modulus (this is, because the exponentiation algorithm need to treat that differently).
// Selecting a modulus2adicity >= 8*modulusByteLength will set it to the maximum meaningful value instead.
//
// The returned benchmarking function records custom entries Gas/op and ns/Gas in addition to the usual ones.
// To select a gas schedule, gasScheduleVersion needs to be one of "EIP2565" or "EIP7833".
// To simplify reading out the ns/Gas value (and not just printing it), e.g. to take a maximum among multiple benchmarks, that value will also be stored in *nsPerGas, unless nsPerGas == nil.
func benchmarkNatExpNN(rand *rand.Rand, baseByteLength uint, modulusByteLength uint, exponentBitLength uint, gasScheduleVersion string, modulus2adicity uint, nsPerGas *float64, slow bool) func(*testing.B) {
	// Setup base, modulus and exponent of the required lengths.
	base, exponent, modulus := createBaseModExp(rand, baseByteLength, modulusByteLength, exponentBitLength, modulus2adicity)

	// compute gas
	gasCost := computeModExpGasSimplified(baseByteLength, modulusByteLength, exponentBitLength, gasScheduleVersion)

	return func(b *testing.B) {
		var z nat = nat{}.set(modulus) // reserve space. We copy the modulus to reserve as much space as the modulus. Note that using nat{}.make() would have us make an assumption on the Word-size.
		for b.Loop() {
			z = z.expNN(nil, base, exponent, modulus, slow)
		}
		b.ReportMetric(float64(gasCost), "Gas/op")
		reportedNsPerGas := float64(b.Elapsed().Nanoseconds()) / (float64(b.N) * float64(gasCost))
		if nsPerGas != nil {
			*nsPerGas = reportedNsPerGas
		}
		b.ReportMetric(reportedNsPerGas, "ns/Gas")
	}
}

// BenchmarkNatExpNN will run a set of benchmarks for nat.expNN for varying input lengths and report each of those. This is a very slow benchmark.
func BenchmarkNatExpNN(b *testing.B) {
	rand := rand.New(rand.NewSource(100))
	var maxGas float64
	for modulusByteLength := 32; modulusByteLength < 320; modulusByteLength += 32 {
		for _, exponentBitLengh := range []uint{1, 2, 3, 4, 5, 6, 7, 8, 32, 40, 48, 56, 64, 96, 128, 256, 384, 512, 1024, 2048, 3 * 1024, 4 * 1024, 5 * 1024} {
			b.Run(fmt.Sprintf("Base%vBytes-Mod%vBytes-Exp%vBit-OddModulus", modulusByteLength, modulusByteLength, exponentBitLengh), benchmarkNatExpNN(rand, uint(modulusByteLength), uint(modulusByteLength), exponentBitLengh, "EIP7883", 0, &maxGas, false))
			b.Run(fmt.Sprintf("Base%vBytes-Mod%vBytes-Exp%vBit-2Adicity1", modulusByteLength, modulusByteLength, exponentBitLengh), benchmarkNatExpNN(rand, uint(modulusByteLength), uint(modulusByteLength), exponentBitLengh, "EIP7883", 1, &maxGas, false))
			b.Run(fmt.Sprintf("Base%vBytes-Mod%vBytes-Exp%vBit-2Adicity8", modulusByteLength, modulusByteLength, exponentBitLengh), benchmarkNatExpNN(rand, uint(modulusByteLength), uint(modulusByteLength), exponentBitLengh, "EIP7883", 8, &maxGas, false))
			if exponentBitLengh <= 64 {
				b.Run(fmt.Sprintf("Base%vBytes-Mod%vBytes-Exp%vBit-OddModulus-SLOW", modulusByteLength, modulusByteLength, exponentBitLengh), benchmarkNatExpNN(rand, uint(modulusByteLength), uint(modulusByteLength), exponentBitLengh, "EIP7883", 0, &maxGas, true))
				b.Run(fmt.Sprintf("Base%vBytes-Mod%vBytes-Exp%vBit-2Adicity1-SLOW", modulusByteLength, modulusByteLength, exponentBitLengh), benchmarkNatExpNN(rand, uint(modulusByteLength), uint(modulusByteLength), exponentBitLengh, "EIP7883", 1, &maxGas, true))
				b.Run(fmt.Sprintf("Base%vBytes-Mod%vBytes-Exp%vBit-2Adicity8-SLOW", modulusByteLength, modulusByteLength, exponentBitLengh), benchmarkNatExpNN(rand, uint(modulusByteLength), uint(modulusByteLength), exponentBitLengh, "EIP7883", 8, &maxGas, true))
			}
		}
	}
	//b.Run("Foo", benchmarkNatExpNN(rand, 256, 256, 1000, "EIP7883", 0, nil))
}
