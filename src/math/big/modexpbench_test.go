package big

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// since the benchmark is extremely slow, we only want to run it if explicitly requested via flag
var benchModExpFlag = flag.Bool("modexp", false, "run ModExp benchmarks")

// DisplayImprovement is an (JSON-serializable) enum type that is used to describe
// whether the user wants to perform a benchmark to compare against previous benchmarking results
// and, if so, whether the differences should be reported as absolute differences or a relative ones.
type DisplayImprovement int

const (
	no_Display       DisplayImprovement = iota // do not diplay differences to a previous benchmark, even if it was possible
	relative_Display                           // display differences to a previous result in relative terms
	absolute_Display                           // display absolute differences to a previous result
)

// String is provided to satisfy the Stringer interface and to JSON-serialize DisplayImprovement as a string.
func (display DisplayImprovement) String() string {
	switch display {
	case no_Display:
		return "None"
	case relative_Display:
		return "Relative"
	case absolute_Display:
		return "Absolute"
	default:
		return "Unknown"
	}
}

// MarshalJSON is provided to satisfy the json.Marshaller interface. Note
// that while we marshal as a captilized string, we are more lenient when unmarshalling.
func (display DisplayImprovement) MarshalJSON() ([]byte, error) {
	return json.Marshal(display.String())
}

// UnmarshalJSON is provided to satisfy the json.Unmarshaller interface.
//
// We are more lenient in what we accept as input than what we output
// (we ignore case and we also accept, "", "False" and "Null")
func (display *DisplayImprovement) UnmarshalJSON(data []byte) (err error) {
	var displayString string
	if err = json.Unmarshal(data, &displayString); err != nil {
		return err
	}
	switch strings.ToUpper(displayString) {
	case "NONE", "", "FALSE", "NULL":
		*display = no_Display
	case "RELATIVE":
		*display = relative_Display
	case "ABSOLUTE":
		*display = absolute_Display
	default:
		err = fmt.Errorf("big: failure parsing JSON for DiplayImprovement. The string %v could not be parsed as a DisplayImprovement", displayString)
	}
	return
}

// Note: We use *uint as a poor man's Optional[uint] with nil meaning "unset".
// This is non-ideal for a lot of reasons (such as giving the wrong default notion of equality), but
// it has the decided advantage that the default JSON-serialization does exactly what we want,
// so we stick to this simple implementation.

// ModExpBenchTestCase defines the parameters used to test our modular exponentiation algorithm.
// Note that this notion of a test case only defines relevant bitlengths rather than
// values for base, exponent and modulus (as would be more natural).
// The reason for that is that the algorithms that are currently implemented only
// care (to the granularity we care about) about bitlengths and number of trailing zeros.
//
// Currently, we perform no randomization of base/exponent/modulus withing a given test case
// (i.e. our b.Loop() reuses the same triple; this could be changes in some further update.
type ModExpBenchTestCase struct {
	ModulusBitLength     uint
	ModulusTrailingZeros *uint // nil for no restriction
	ExponentBitLength    uint
	BaseBitLength        *uint // nil for "same as ModulusBitLength"
	BaseTrailingZeros    *uint // nil for no restriction
}

// Eq checks two ModExpBenchTestCase for equality.
//
// We we consider a nil BaseBitLength and a BaseBitLength explicitly set to ModulusBitLength as equal.
func (t1 *ModExpBenchTestCase) Eq(t2 *ModExpBenchTestCase) bool {

	// compare two optional uints
	cmpOptionalUint := func(z1 *uint, z2 *uint) bool {
		if z1 == nil && z2 == nil {
			return true
		}
		if z1 == nil {
			return false
		}
		if z2 == nil {
			return false
		}
		return *z1 == *z2
	}

	var (
		baseBit1 = t1.ModulusBitLength
		baseBit2 = t2.ModulusBitLength
	)

	if t1.BaseBitLength != nil {
		baseBit1 = *t1.BaseBitLength
	}
	if t2.BaseBitLength != nil {
		baseBit2 = *t2.BaseBitLength
	}
	return t1.ModulusBitLength == t2.ModulusBitLength &&
		cmpOptionalUint(t1.ModulusTrailingZeros, t2.ModulusTrailingZeros) &&
		t1.ExponentBitLength == t2.ExponentBitLength &&
		cmpOptionalUint(t1.BaseTrailingZeros, t2.BaseTrailingZeros) &&
		baseBit1 == baseBit2

}

// makeBenchmark creates a benchmarking function from the given ModExpBenchTestCase,
// to be used with [*testing.B.Run] or [testing.Benchmark].
// If resetMemory is set, we bypass the use of the (global) sync.Pool for the stacks that hold temporaries
// and allocate a new stack in each loop iteration.
//
// The resulting benchmarking function will only run a benchmark, but *not* add any
// extra comparison data such as requested by params.CostMetric, params.TimeImprovements, params.MemImprovements or params.AllocImprovements.
// The reason for this is that [testing] does not provide any API to measure memory costs from within the benchmarking function itself.
// (as opposed to [testing.B.Elapsed] for time). So we will need to post-process the resulting BenchmarkResult.
// Note that this essentially means that we need to use (the more complicated) [testing.Benchmark] rather than [*testing.B.Run],
// as the latter directly prints the benchmark result and gives us no way to add data.
func (testCase *ModExpBenchTestCase) makeBenchmark(rnd *rand.Rand, params *ModExpBenchInput) func(b *testing.B) {
	// create instance: we do this outside of the returned function in order to not contribute to the measured memory consumption.
	bitLength := int(testCase.ModulusBitLength)
	var trailingZeros []uint = nil
	if testCase.ModulusTrailingZeros != nil {
		trailingZeros = []uint{*testCase.ModulusTrailingZeros}
	}
	modulus := createRandomInstance(rnd, bitLength, trailingZeros...)
	if testCase.BaseBitLength != nil {
		bitLength = int(*testCase.BaseBitLength)
	}
	if testCase.BaseTrailingZeros != nil {
		trailingZeros = []uint{*testCase.BaseTrailingZeros}
	} else {
		trailingZeros = nil
	}
	base := createRandomInstance(rnd, bitLength, trailingZeros...)
	exponent := createRandomInstance(rnd, int(testCase.ExponentBitLength))

	// initialize memory large enough (or even larger) to hold the result
	var z nat = make(nat, (testCase.ModulusBitLength/_W)+1)

	return func(b *testing.B) {

		// turn on memory benchmarking, even if -benchmem was not given globally.
		if params.AllocImprovements != no_Display || params.MemImprovements != no_Display {
			b.ReportAllocs()
		}

		// stack
		var stk *stack = nil

		for b.Loop() {
			if params.ResetMemory {
				stk = new(stack)
			}
			z.expNN(stk, base, exponent, modulus, false)
		}
	}
}

// ModExpBenchInput is used to collect the data that we need to collect the input to a benchmarking request.
type ModExpBenchInput struct {
	ResetMemory bool   // wipe the stack after each invocation.
	Filename    string // filename used for output

	// we allow two different ways of defining a set of test cases:
	// If ExponentLengths, ModulusLengths and ModulusTrailingZeros all have len > 0,
	// we run a test on each of the len(ExponentLengths) * len(MoudlusLengths) * len(ModulusTrailingZeros)
	// combinations.
	// Additionally, we can specify FurtherTestCases as a simple list of test cases. If you use
	// both ways, we run both in succession.
	//
	// Note that the first way with ExponentLengths,ModulusLengths,ModulusTrailingZeros
	// allows a different way to convert the result into a CSV - table.
	ExponentLengths      []uint
	ModulusLengths       []uint
	ModulusTrailingZeros []*uint

	FurtherTestCases []ModExpBenchTestCase

	Metrics []CostMetric

	TimeImprovements  DisplayImprovement
	MemImprovements   DisplayImprovement
	AllocImprovements DisplayImprovement

	// Note: Rather than a map ModExpBenchTestCase -> Results, we use a slice here (where the Result type contains the
	// TestCase used to create it). This is because
	// (a) the default equality notion for ModExpBenchTestCase is wrong
	// (b) we might have duplicate test cases.

	Results []Result // When struct-embedded in ModExpBenchOutput, this holds the result of our benchmarks.
	// We define it in ModExpBenchInput (rather than output), because we want deserialize into it; in this case, it is used to
	// initialize oldResults, which is used for relative benchmarking.

	oldResults []Result `json:"-"`
}

var exampleInput ModExpBenchInput = ModExpBenchInput{
	ResetMemory:          false,
	Filename:             "example-bench",
	ExponentLengths:      []uint{1, 2, 3, 4, 5, 6, 7, 8, 16, 24, 32, 64, 128, 256, 512, 1024},
	ModulusLengths:       []uint{8, 16, 32, 64, 128, 196, 256, 512, 1024, 2048},
	ModulusTrailingZeros: []*uint{nil},
	TimeImprovements:     relative_Display,
	MemImprovements:      relative_Display,
	AllocImprovements:    absolute_Display,
}

type CostMetric interface {
	// json.Marshaler
	// json.Unmarshaler
	Cost(*ModExpBenchTestCase) float64
	CostName() *string
	MetricName() *string
}

// Note: the intended(?) way to add extra benchmarking information via [*testing.B.ReportMetric] is not
// an option, since we can only do this from within the benchmarking function f(testing.*B) itself.
// However, inside of f, we have no API to measure the memory consumption of f, which we may need.
// So we instead post-process the resulting benchmarkResult of type [testing.BenchmarkResult] from the call to [testing.Benchmark](f)
// and directly modify benchmarkResult.Extra.
// Note that this means that the special cases and checks performed by ReportMetric are skipped;
// It is up to z to ensure that e.g. z.CostMetric contains no whitespace.

// PostprocessBenchmarkResult post-processes the given benchmark result by adding extra data to
// benchmarkResult.Extra
func (z *ModExpBenchInput) PostprocessBenchmarkResult(testcase *ModExpBenchTestCase, benchmarkResult *testing.BenchmarkResult) {
	for _, metric := range z.Metrics {
		if metric == nil {
			continue
		}
		costName := metric.CostName()
		relativeCostName := metric.MetricName()
		cost := metric.Cost(testcase)
		if costName != nil {
			benchmarkResult.Extra[*costName] = cost
		}
		if relativeCostName != nil {
			timePerCost := float64(benchmarkResult.NsPerOp()) / cost
			benchmarkResult.Extra[*relativeCostName] = timePerCost
		}
	}

	if z.TimeImprovements == no_Display && z.MemImprovements == no_Display && z.AllocImprovements == no_Display {
		return
	}

	// search for an entry in z.oldResults with the same parameters.
	// We use the first instance found in case of duplicates
	found := false
	var oldResult Result
	for _, oldResult = range z.oldResults {
		if oldResult.ModExpBenchTestCase.Eq(testcase) {
			found = true
			break
		}
	}
	if !found {
		return
	}
	switch z.TimeImprovements {
	case no_Display:
		// do nothing
	case absolute_Display:
		// Note that these are int64, not float64.
		oldValue := oldResult.NsPerOp()
		newValue := benchmarkResult.NsPerOp()
		benchmarkResult.Extra["GainedNS/op"] = float64(oldValue - newValue)
	case relative_Display:
		oldValue := oldResult.NsPerOp()
		newValue := benchmarkResult.NsPerOp()
		benchmarkResult.Extra["RelativeImprovement(ns/op)"] = float64(oldValue-newValue)/float64(oldValue) - 1.0
	default:
		panic("big: unrecognized value for TimeImprovements")
	}
	switch z.AllocImprovements {
	case no_Display:
		// do nothing
	case absolute_Display:
		oldValue := oldResult.AllocsPerOp()
		newValue := benchmarkResult.AllocsPerOp()
		benchmarkResult.Extra["SavedAllocs/op"] = float64(oldValue - newValue)
	case relative_Display:
		oldValue := oldResult.AllocsPerOp()
		newValue := benchmarkResult.AllocsPerOp()
		benchmarkResult.Extra["RelativeImprovement(allocs/op)"] = float64(oldValue-newValue)/float64(oldValue) - 1.0
	default:
		panic("big: unrecognized value for AllocImprovements")
	}
	switch z.MemImprovements {
	case no_Display:
		// do nothing
	case absolute_Display:
		oldValue := oldResult.AllocedBytesPerOp()
		newValue := benchmarkResult.AllocedBytesPerOp()
		benchmarkResult.Extra["SavedBytes/op"] = float64(oldValue - newValue)
	case relative_Display:
		oldValue := oldResult.AllocedBytesPerOp()
		newValue := benchmarkResult.AllocedBytesPerOp()
		benchmarkResult.Extra["RelativeImprovement(bytes/op)"] = float64(oldValue-newValue)/float64(oldValue) - 1.0
	default:
		panic("big: unrecognized value for MemImprovements")
	}

}

func (z *ModExpBenchInput) processAllCases(rnd *rand.Rand) {
	z.oldResults = z.Results // save old results
	tableSize := len(z.ExponentLengths) * len(z.ModulusLengths) * len(z.ModulusTrailingZeros)
	z.Results = make([]Result, 0, tableSize+len(z.FurtherTestCases))
	for _, modulusLength := range z.ModulusLengths {
		for _, exponentLength := range z.ExponentLengths {
			for _, trailingZeros := range z.ModulusTrailingZeros {
				testCase := ModExpBenchTestCase{
					ModulusBitLength:     modulusLength,
					ModulusTrailingZeros: trailingZeros,
					ExponentBitLength:    exponentLength,
					BaseBitLength:        &modulusLength,
					BaseTrailingZeros:    nil,
				}
				result := testing.Benchmark(testCase.makeBenchmark(rnd, z))
				z.PostprocessBenchmarkResult(&testCase, &result)

				z.Results = append(z.Results, Result{ModExpBenchTestCase: testCase, BenchmarkResult: result})
			}
		}
	}
}

type Result struct {
	ModExpBenchTestCase
	testing.BenchmarkResult
}

type ModExpBenchOutput struct {
	ModExpBenchInput

	StartTime time.Time
	EndTime   time.Time

	Name string
}

func TestXXX(t *testing.T) {
	if !*benchModExpFlag {
		t.SkipNow()
	}
}
