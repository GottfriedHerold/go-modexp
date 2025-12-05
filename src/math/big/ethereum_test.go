package big

// This file defines some cost metrics to measure the performance of the modular exponentiation algorithm against.
// Notably, we care about how the computational cost of computing base**exponent mod modulus behaves, compared to
// some function Cost(params), where params defines the bitlength of base, exponent and modulus (and potentially, the number of trailing zeros)
//
// See modexpbench_test.go for the benchmarking framework we use for this, which
// defines a CostMetric struct with a RegisterCostMetric() method that we use for this purpose.
//
// The cost metrics that we define here come from the context of the Ethereum blockchain.
// In this blockchain, users can ask the Ethereum network to perform a modular exponentian computation on their behalf,
// and the network charges them (in "gas" units) for this computation. To ensure fairness and to avoid DoS attacks,
// the charged gas should match the actual computational cost. The actual formula for the gas computation on Ethereum is subject to
// change. We roughly reproduce EIP7883 and EIP2565 here, which are the relevant versions of formulas that we care about.
// Note that there are slight simplifications in our formulas here, compared to what Ethereum actually uses.
// These differences are due to potential (large numbers of) leading zeros and encoding the actual data;
// we do not consider these aspects for benchmarking nat.ModExp, because nat never has leading zeros as an internal invariant
// (except possibly for Montgomery multiplication, which uses fixed width and should be thought of as acting on a separate type),
// so these decoding aspects have been handled by a different layer in the code than the modular exponentiation itself.

func computeCostEIP2565(testCase *ModExpBenchTestCase) float64 {
	if testCase.ModulusBitLength == 0 {
		panic("called computeCostEIP2565 with modulus of length 0") // corresponds to exponentiation in the integers and is not covered.
	}
	var maxLen uint = testCase.ModulusBitLength
	if testCase.BaseBitLength != nil {
		maxLen = max(maxLen, *testCase.BaseBitLength)
	}

	baseWords := (maxLen + 63) / 64                   // number of 64-bit words in max{modulus, base}
	multiplicationComplexity := baseWords * baseWords // complexity for a single multiplication
	var iteration_count uint64
	if testCase.ExponentBitLength <= 1 {
		iteration_count = 1
	} else {
		iteration_count = uint64(testCase.ExponentBitLength) - 1
	}
	result := max((iteration_count*uint64(multiplicationComplexity))/2, 200)
	return float64(result)
}

func computeCostEIP7883(testCase *ModExpBenchTestCase) float64 {
	if testCase.ModulusBitLength == 0 {
		panic("called computeCostEIP7883 with modulus of length 0") // corresponds to exponentiation in the integers and is not covered.
	}
	var maxLen uint = testCase.ModulusBitLength
	if testCase.BaseBitLength != nil {
		maxLen = max(maxLen, *testCase.BaseBitLength)
	}
	baseWords := (maxLen + 63) / 64 // number of 64-bit words in max{modulus, base}
	var multiplicationComplexity uint64 = 16
	if baseWords > 4 {
		multiplicationComplexity = 2 * uint64(baseWords) * uint64(baseWords) // strangely discontinuous, but that's what the EIP says.
	}
	var iteration_count uint64 = 1
	if testCase.ExponentBitLength <= 1 {
		iteration_count = 1
	} else if testCase.ExponentBitLength <= 256 {
		iteration_count = uint64(testCase.ExponentBitLength - 1)
	} else { // ExponentBitLength >= 257
		iteration_count = uint64(testCase.ExponentBitLength-1) + 8*((uint64(testCase.ExponentBitLength)+7)/8-32)
	}
	result := max(multiplicationComplexity*iteration_count, 500)
	return float64(result)
}

var (
	gasCostString   = "gas"
	gasMetricString = "ns/gas"
)

var _ *CostMetric = (&CostMetric{
	JSONString: "EIP2565",
	Cost:       computeCostEIP2565,
	CostName:   &gasCostString,
	MetricName: &gasMetricString,
}).RegisterCostMetric()

var _ *CostMetric = (&CostMetric{
	JSONString: "EIP7883",
	Cost:       computeCostEIP7883,
	CostName:   &gasCostString,
	MetricName: &gasMetricString,
}).RegisterCostMetric()
