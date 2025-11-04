package big

// NOTE: This file really should be a separate package, for both clarity and to avoid cyclic dependencies.
// The only reason it is not is that it needs the stack type and access to the corresponding (non-exported) global variables in nat.go.

import (
	csvencoding "encoding/csv" // calibrate_test.go already defines a csv function.
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

// since the benchmark is extremely slow, we only want to run it if explicitly requested via flag.
// The flag's value is a filename that contains the input specification.
var benchModExpFlag = flag.String("modexp", "", "run ModExp benchmarks specified by file")

var (
	jsonOutFlag = flag.String("jsonout", "", "output ModExp benchmarks into this json file")
	// name is an arbitrary user-provided opaque string whose whole purpose is to allow users to record any data
	// about the benchmark.
	benchnameFlag = flag.String("name", "", "name of benchmark")
	csvOutFlag    = flag.String("csvout", "", "output ModExp benchmarks into this csv file")
	csvMetric     = flag.String("csvmetric", "", "output ModExp benchmark as csv only for this metric")
)

// Our benchmarks will include information about the test case as extra metrics in their
// [testing.BenchmarkResult].Extra data under those keys.
const (
	MetricModulusBitLen        = "bitlen(modulus)"
	MetricExponentBitLen       = "bitlen(exponent)"
	MetricBaseBitLen           = "bitlen(base)"
	MetricModulusTrailingZeros = "trailingZeros(modulus)" // optional.
)

// DisplayImprovement is a JSON-serializable enum type that is used to describe
// whether the user wants to perform a benchmark to compare against previous benchmarking results
// and, if so, whether the differences should be reported as absolute or relative differences.
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
// Currently, we perform no randomization of base/exponent/modulus within a given test case,
// i.e. our b.Loop() reuses the same triple; this could be changed in some further update.
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

// toBenchmark creates a benchmarking function from the given ModExpBenchTestCase,
// to be used with [*testing.B.Run] or [testing.Benchmark].
//
// The resulting benchmarking function will only run a benchmark, but *not* add any
// extra comparison data such as requested by params.Metrics, params.TimeImprovements, params.MemImprovements or params.AllocImprovements.
// The reason for this is that [testing] does not provide any API to measure memory costs from within the benchmarking function itself.
// (as opposed to [*testing.B.Elapsed] for time). So we will need to post-process the resulting BenchmarkResult.
// This essentially means that we need to use (the more complicated) [testing.Benchmark] rather than [*testing.B.Run],
// as the latter directly prints the benchmark result and gives us no way to add data.
func (testCase *ModExpBenchTestCase) toBenchmark(rnd *rand.Rand, params *ModExpBenchInput) func(b *testing.B) {
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

		if params.ResetMemory {
			for b.Loop() {
				stk = new(stack)
				z.expNN(stk, base, exponent, modulus, false)
			}
		} else {
			for b.Loop() {
				z.expNN(stk, base, exponent, modulus, false)
			}
		}

		// report relevant input parameters in the benchmark output itself.
		b.ReportMetric(float64(testCase.ModulusBitLength), MetricModulusBitLen)
		b.ReportMetric(float64(testCase.ExponentBitLength), MetricExponentBitLen)
		if testCase.BaseBitLength != nil {
			b.ReportMetric(float64(*testCase.BaseBitLength), MetricBaseBitLen)
		}
		if testCase.ModulusTrailingZeros != nil {
			b.ReportMetric(float64(*testCase.ModulusTrailingZeros), MetricModulusTrailingZeros)
		}
	}
}

// ModExpBenchInput is used to collect the data that we need to collect the input to a benchmarking request.
// To run our benchmark, the user needs to provided a concrete ModExpBenchInput via a JSON-file.
//
// Note that a few select fields of the ModExpBenchInput that are provided via JSON-file can be overridden by
// command-line arguments.
type ModExpBenchInput struct {
	ResetMemory bool // use a fresh stack for each invocation.

	// we allow two different ways of defining a set of test cases:
	//
	// 		- If ExponentLengths, ModulusLengths and ModulusTrailingZeros all have len > 0,
	// 		  we run a test on each of the len(ExponentLengths) * len(MoudlusLengths) * len(ModulusTrailingZeros)
	// 		  combinations.
	// 		- Additionally, we can specify FurtherTestCases as a simple list of test cases. If you use
	// 		  both ways, we run both in succession.
	//
	// Note that the first way with ExponentLengths,ModulusLengths,ModulusTrailingZeros
	// allows a different way to convert the result into a CSV - table for tabulating how a given metric depends on the input parameters.
	// The CSVTableForMetrics field enables that feature, which disregards FurtherTestCases.
	ExponentLengths      []uint
	ModulusLengths       []uint
	ModulusTrailingZeros []*uint
	FurtherTestCases     []ModExpBenchTestCase

	// Metrics defines a list of additional CostMetrics to collect.
	// For each *CostMetric, we additionally collect some cost function Cost(testcase)
	// and we may add cost and time/cost to the extra output of the benchmark.
	// See [CostMetric] for details.
	Metrics []*CostMetric

	// TimeImprovements, MemImprovement, AllocImprovements constrol whether
	// we should compare the benchmark result to a previous benchmark result already stored in Results.
	TimeImprovements  DisplayImprovement
	MemImprovements   DisplayImprovement
	AllocImprovements DisplayImprovement

	// Note: Rather than a map ModExpBenchTestCase -> Results, we use a slice here (where the Result type contains the
	// TestCase used to create it). This is because
	// (a) the default equality notion for ModExpBenchTestCase is wrong
	// (b) we might have duplicate test cases.

	Results []Result // When struct-embedded in [ModExpBenchOutput], Results holds the result of our benchmarks.
	// We define it in ModExpBenchInput (rather than [ModExpBenchOutput]), because we want deserialize into it; in this case, it is used to
	// initialize oldResults, which is used for relative benchmarking.

	// When non-empty, Name defines a custom name that is printed and included in the output. Can be overwritten by command-line arg "name"
	// This can be used to record the context of the benchmark. We recommend including the hash of the commit that the benchmark was run on here.
	// Also, consider including something that identifies (the spec of) the machine the benchmark was run on.
	Name string

	RndSeed *int64 // optional RND seed. If nil, we derive one from the current time.

	oldResults []Result `json:"-"` // if we deserialize and initialize with Results already filled, we copy those into oldResults.

	DisplayResults   bool   // whether we output results to stdout
	JSONOut          string // if set to a non-empty string, we write JSON to this filename. Can be overwritten by command-line arg
	JSONOutOverwrite bool   // if set, we overwrite existing files. Otherwise, we append a suffix to create a new filename.
	CSVOut           string // if set to a non-empty string, we write CSV to this filename. Can be overwritten by command-line arg
	CSVOutOverwrite  bool   // if set, we overwrite existing files. Otherwise, we append a suffix to create a new filename.

	// At least one of those needs to be set if we want meaningful CSV output
	CSVMeta bool // if set, CSV output starts with some (non-table) entries that identify the benchmark
	CSVAll  bool // if set, CSV output contains a table with one row per benchmark
	// for each metric in this slice, CSV output contains an exponents x moduli table, where each table entry is only a single metric.
	// This ignores FurtherTestCases and only looks at ExponentLength, ModulusLengths, ModulusTrailingZeros.
	// CSVTableForMetrics can be overwritten by command-line. In the latter case, we only consider a single metric.
	CSVTableForMetrics []string
}

// Result hold the result of runing a benchmark on a single ModExpBenchTestCase
type Result struct {
	// We include the parameters used to create the benchmark here as part of the Result.
	//
	// The (more natural) alternative of using a map[ModExpBenchTestCase] testing.BenchmarkResult does not work, because we might have duplicate ModExpBenchTestCases;
	// So we use []Result, with Result including the ModExpBenchTestCase to store our results.
	ModExpBenchTestCase
	testing.BenchmarkResult
}

// ModExpBenchOutput is the struct that holds the result of running a benchmark with inputs specified by some
// [ModExpBenchInput]. We retain a copy of the input parameters in ModExpBenchOutput. The [ModExpBenchInput] is struct-embedded
// to simplify serializing a ModExpBenchOutput and later deserializing as a ModExpBenchInput.
type ModExpBenchOutput struct {
	ModExpBenchInput           // For technical reasons (differential benchmarks), the actual results are stored in ModExpBenchInput.
	StartTime        time.Time // Start time of benchmark
	EndTime          time.Time // Finish time of benchmark
}

// TODO: Remove this?

// exampleInput is just used to create an example JSON-file, which can then be modified.
// (via the commended-out Test below)
var exampleInput ModExpBenchInput = ModExpBenchInput{
	ResetMemory:          false,
	ExponentLengths:      []uint{1, 2, 3, 4, 5, 6, 7, 8, 16, 24, 32, 64, 128, 256, 512, 1024},
	ModulusLengths:       []uint{8, 16, 32, 64, 128, 196, 256, 512, 1024, 2048},
	ModulusTrailingZeros: []*uint{nil},
	TimeImprovements:     relative_Display,
	MemImprovements:      relative_Display,
	AllocImprovements:    absolute_Display,
	Metrics:              []*CostMetric{},
}

/*
func TestWriteExample(t *testing.T) {
	outfile, err := os.OpenFile("example-config.json", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer outfile.Close()
	outstring, err := json.MarshalIndent(exampleInput, "", "\t")
	if err != nil {
		panic(err)
	}
	_, err = outfile.Write(outstring)
	if err != nil {
		panic(err)
	}
}
*/

// CostMetric is a struct that specifies a cost metric to compare the computational time of modular exponentiation against.
// By including a CostMetric in [ModExpBenchInput], we trigger additional outputs for benchmarks.
//
// Notably, Cost defines a cost function and in our benchmark for a given testcase,
// we output Cost(testcase) and time_taken / Cost(testcase) as extra outputs.
// Cost must be non-nil and not modify its input.
// CostName is the name associated with Cost(testcase)
// and MetricName is the name associated with time_taken / Cost(testcase).
// Either of those can be nil; in this case, we skip the corresponding output.
// If non-nil, CostName resp. MetricName must be non-empty and contain no whitespace,
// matching the restrictions of [*testing.B.ReportMetric]
//
// JSONString is the string output when JSON-serializing a CostMetric. To
// deserialize from the string, you must call [RegisterCostMetric]
//
// We expect costMetrics to be defined as immutable global variables as
// var _ *CostMetric = (&CostMetric{...}).RegisterCostMetric()
type CostMetric struct {
	JSONString string                             // how this should be serialized as a string
	Cost       func(*ModExpBenchTestCase) float64 // Cost function to compute cost from the testcase parameters.
	CostName   *string                            // unit to display for Cost(testcase). If nil, do not display
	MetricName *string                            // unit to display for time / Cost(testcase). If nil, do not display.
}

// We hold a global map JSONString -> *CostMetric used for (de)serialization.
// This map is populated when we define *CostMetrics via var _ = (&CostMetric{...}).RegisterCostMetric()
var (
	registeredCostMetrics map[string]*CostMetric = make(map[string]*CostMetric)
	costMetricMutex       sync.Mutex             // probably not needed, actually.
)

// RegisterCostMetric registers the given cost metric for JSON deserialization, so the deserializer registers the JSON string.
// This needs to be called (at least) once for every *CostMetric. It returns the receiver.
//
// We require that the metric.JSONString values for every registered metric are non-empty and distinct, otherwise this function panics.
// Registering the same CostMetric twice works (and is a no-op), but has to use a pointer to the same object (rather than to a copy).
//
// This is intenteded to be called on (global) *CostMetrics at the time of definition via
// var _ *CostMetric = (&CostMetric{...}).RegisterCostMetric()
//
// If metric is invalid, this function panics.
func (metric *CostMetric) RegisterCostMetric() *CostMetric {

	jsonName := metric.JSONString

	// This function panics rather than reporting an error.
	// Since this is intended to be run solely during global variable initialization,
	// this is acceptable.
	if len(jsonName) == 0 {
		panic("big: called RegisterCostMetric with a CostMetric without a jsonString")
	}

	if metric.Cost == nil {
		panic("big: called RegisterCostMetric with an invalid CostMetric that has a nil Cost function")
	}

	// we perform the same checks as (*testing.B).ReportMetric to remain constistent with the latter.
	// Code taken and adapted from there.
	// Note that we do not treat the special-case "ns/op" here.
	// This is handled by the actual display functions rather than ReportMetric (albeit that fact is undocumented).
	if metric.CostName != nil {
		if *metric.CostName == "" {
			panic("big: called RegisterCostMetric with a CostMetric with pointer to empty CostName string")
		}

		if strings.IndexFunc(*metric.CostName, unicode.IsSpace) >= 0 {
			panic("big: called RegisterCostMetric with a CostMetric that contains whitespace")
		}
	}
	if metric.MetricName != nil {
		if *metric.MetricName == "" {
			panic("big: called RegisterCostMetric with a CostMetric with pointer to empty MetricName string")
		}

		if strings.IndexFunc(*metric.MetricName, unicode.IsSpace) >= 0 {
			panic("big: called RegisterCostMetric with a MetricName that contains whitespace")
		}
	}

	costMetricMutex.Lock()
	defer costMetricMutex.Unlock()
	previous, exists := registeredCostMetrics[jsonName]
	if exists {
		if previous != metric {
			panic("big: called RegisterCostMetric for already registered Cost metric with a different cost metric")
		} else {
			return metric
		}
	}
	registeredCostMetrics[jsonName] = metric
	return metric
}

func (metric *CostMetric) MarshalJSON() ([]byte, error) {
	return json.Marshal(metric.JSONString)
}

func (metric *CostMetric) UnmarshalJSON(data []byte) (err error) {
	var s string
	err = json.Unmarshal(data, &s)
	if err != nil {
		return
	}
	costMetricMutex.Lock()
	defer costMetricMutex.Unlock()
	m, found := registeredCostMetrics[s]
	if !found {
		err = fmt.Errorf("big: %v was not recognized as a CostMetric when JSON-unmarshalling. Did you forget to call RegisterCostMetric?", s)
		return
	}
	*metric = *m
	return
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
		costName := metric.CostName
		relativeCostName := metric.MetricName
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
		benchmarkResult.Extra["%Improvement(ns/op)"] = 100.0 * float64(oldValue-newValue) / float64(oldValue)
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
		if oldValue != 0 {
			benchmarkResult.Extra["%Improvement(allocs/op)"] = 100.0 * float64(oldValue-newValue) / float64(oldValue)
		}
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
		if oldValue != 0 {
			benchmarkResult.Extra["%Improvement(bytes/op)"] = 100.0 * float64(oldValue-newValue) / float64(oldValue)
		}
	default:
		panic("big: unrecognized value for MemImprovements")
	}

}

func (z *ModExpBenchInput) processAllCases(rnd *rand.Rand) {
	z.oldResults = z.Results // save old results
	tableSize := len(z.ExponentLengths) * len(z.ModulusLengths) * len(z.ModulusTrailingZeros)
	z.Results = make([]Result, 0, tableSize+len(z.FurtherTestCases))
	// Note: The CSV output methods may rely on the ordering of the loops here.
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
				result := testing.Benchmark(testCase.toBenchmark(rnd, z))
				z.PostprocessBenchmarkResult(&testCase, &result)

				z.Results = append(z.Results, Result{ModExpBenchTestCase: testCase, BenchmarkResult: result})
			}
		}
	}
	for _, testCase := range z.FurtherTestCases {
		result := testing.Benchmark(testCase.toBenchmark(rnd, z))
		z.PostprocessBenchmarkResult(&testCase, &result)
		z.Results = append(z.Results, Result{ModExpBenchTestCase: testCase, BenchmarkResult: result})
	}
}

func (z *ModExpBenchInput) RunBenchmarks(rnd *rand.Rand) ModExpBenchOutput {
	result := ModExpBenchOutput{ModExpBenchInput: *z}
	result.StartTime = time.Now()
	result.processAllCases(rnd)
	result.EndTime = time.Now()
	return result
}

// getMetric returns the relevant metric from the benchmark result. The result will be of type
// int64, int, float64 or nil (if not present).
//
// This method essentially is just for unifying the predefined metrics "ns/op", "B/op", "allocs/op", "N", for which
// [testing.BenchmarkResult] has a different interface, with any extra metrics.
func (result *Result) getMetric(metric string) any {
	switch metric {
	case "ns/op":
		return result.NsPerOp()
	case "B/op":
		return result.AllocedBytesPerOp()
	case "allocs/op":
		return result.AllocsPerOp()
	case "N":
		return result.N
	default:
		value, ok := result.Extra[metric]
		if !ok {
			return nil
		} else {
			return value
		}
	}
}

// getTable creates and returns a 3-dimensional table
// indexed by ModulusLengths x ExponentLengths x ModulusTrailingZeros
// of the corresponding metric.
// The table entries are either int64 (for "ns/op", "B/op", "allocs/op"), float64 (for custom metrics), int (for "N")
// or nil (if the metric was not present)
func (z *ModExpBenchOutput) getTable(metric string) (outputTable [][][]any) {
	var i = 0
	outputTable = make([][][]any, len(z.ModulusLengths))
	// ordering of for loops must match the processAllCases method
	for modulusLengthIndex := range z.ModulusLengths {
		outputTable[modulusLengthIndex] = make([][]any, len(z.ExponentLengths))
		for exponentLengthIndex := range z.ExponentLengths {
			outputTable[modulusLengthIndex][exponentLengthIndex] = make([]any, len(z.ModulusTrailingZeros))
			for trailingZerosIndex := range z.ModulusTrailingZeros {
				outputTable[modulusLengthIndex][exponentLengthIndex][trailingZerosIndex] = z.Results[i].getMetric(metric)
			}
		}
	}
	return
}

// WriteCSVTable writes a table (with rows corresponding to moduli and columns corresponding to exponents) of results for metric to out.
// The table includes labels in the first row / column denoting the bitlengths of moduli resp. exponents.
//
// If the modulus has a prescribed number of trailing zeros, the output format is "TotalNumberOfBits(NumberOfTrailingZeros)" for the row labels.
func (z *ModExpBenchOutput) WriteCSVTable(out io.Writer, metric string) error {
	TableRows := len(z.ModulusLengths) * len(z.ModulusTrailingZeros) // exclusing header line
	TableCols := len(z.ExponentLengths)                              // excluding header column
	TableGroupSize := len(z.ModulusTrailingZeros)
	table := make([][]string, TableRows+1)
	for i := 0; i < TableRows+1; i++ {
		table[i] = make([]string, TableCols+1)
	}

	// write 0,0 entry
	table[0][0] = "Modulus\\Exponent"
	// write header row:
	for i, exponentLength := range z.ExponentLengths {
		table[0][i+1] = strconv.Itoa(int(exponentLength))
	}

	// write header column
	for j1, modulusLength := range z.ModulusLengths {
		for j2, modulusTrailingZero := range z.ModulusTrailingZeros {
			if modulusTrailingZero == nil {
				table[j1*TableGroupSize+j2+1][0] = strconv.Itoa(int(modulusLength))
			} else {
				table[j1*TableGroupSize+j2+1][0] = fmt.Sprintf("%v(%v)", modulusLength, *modulusTrailingZero)
			}
		}
	}

	// fill table:
	rawTable := z.getTable(metric)
	for i, _ := range z.ExponentLengths {
		for j1, _ := range z.ModulusLengths {
			for j2, _ := range z.ModulusTrailingZeros {
				val := rawTable[j1][i][j2]
				if val == nil {
					table[j1*TableGroupSize+j2+1][i+1] = "N/A"
				} else {
					table[j1*TableGroupSize+j2+1][i+1] = fmt.Sprintf("%v", val) // float64 or some integer type
				}
			}
		}
	}

	csvWriter := csvencoding.NewWriter(out)
	return csvWriter.WriteAll(table)
}

// WriteMetaAsCSV writes metadata about the test run to out in CSV format.
// The rows written might not all have the same number of entries. This is mostly intended for
// importing into other software (such as Excel or LibreOffice Calc), where this does not matter much.
func (z *ModExpBenchOutput) WriteMetaAsCSV(out io.Writer) (err error) {
	csvWriter := csvencoding.NewWriter(out)
	f := func(values ...string) error {
		return csvWriter.Write(values)
	}
	err = f("Name", z.Name)
	if err != nil {
		return
	}
	err = f("Start Time", z.StartTime.String(), "End Time", z.EndTime.String())
	if err != nil {
		return
	}
	csvWriter.Flush()
	err = csvWriter.Error()
	return
}

// OutputAsCSV outputs all result entries in CSV format, writing to the provided io.Writer.
// The output starts with a "header line" identifying what the columns are, then 1 line per result.
func (z *ModExpBenchOutput) OutputAsCSV(out io.Writer) (err error) {

	// figure out what the actual columns of the table should be.
	// We want to include all metrics that appear in any of the results, grouped by "type".

	// build union of all the z.Results[i].Extra maps.
	allKeys := make(map[string]bool)
	for _, result := range z.Results {
		for key, _ := range result.Extra {
			allKeys[key] = true
		}
	}

	instanceKeys := []string{MetricBaseBitLen, MetricModulusBitLen, MetricExponentBitLen}
	baseMetrics := []string{"N", "ns/op", "B/op", "allocs/op"} // special-case if they appear in extra.
	improvementMetrics := []string{}
	otherMetrics := []string{}
	allMetrics := []string{}
	for key, _ := range allKeys {
		switch key {
		case MetricBaseBitLen, MetricModulusBitLen, MetricExponentBitLen:
			// do nothing, already included.
		case MetricModulusTrailingZeros:
			instanceKeys = append(instanceKeys, key)
		case "N", "ns/op", "B/op", "allocs/op":
			// do nothing, already included.
		case "GainedNS/op", "%Improvement(ns/op)", "SavedAllocs/op", "%Improvement(allocs/op)", "SavedBytes/op", "%Improvement(bytes/op)":
			improvementMetrics = append(improvementMetrics, key)
		default:
			otherMetrics = append(otherMetrics, key)
		}
	}
	sort.Strings(improvementMetrics)
	sort.Strings(otherMetrics)
	allMetrics = append(allMetrics, instanceKeys...)
	allMetrics = append(allMetrics, baseMetrics...)
	allMetrics = append(allMetrics, improvementMetrics...)
	allMetrics = append(allMetrics, otherMetrics...)

	numMetrics := len(allMetrics)

	outputTable := make([][]string, len(z.Results)+1)
	for i := 0; i < len(z.Results)+1; i++ {
		outputTable[i] = make([]string, numMetrics)
	}

	// header line
	outputTable[0] = allMetrics

	for i, result := range z.Results {
		for j, metric := range allMetrics {
			var tableEntry any = result.getMetric(metric)
			if tableEntry == nil {
				outputTable[i+1][j] = "N/A"
			} else {
				outputTable[i+1][j] = fmt.Sprintf("%v", tableEntry) // some float or int type
			}
		}
	}

	csvWriter := csvencoding.NewWriter(out)
	return csvWriter.WriteAll(outputTable)

}

// TestBenchmarkModExp is the actual benchmarking function.
//
// This is supposed to be run as
//
//	go test -run=BenchmarkModExp -modexp=INPUTFILE -name=NAME -jsonout=OUTFILE -csvout=OUTFILE2 -csvmetric=METRIC
//
// The modexp parameter is mandatory and needs to specify a JSON file. This file controls the parameters of the benchmark.
// -modexp doubles as a flag whether to even run this (expensive) benchmark at all, so we silently skip the benchmark if it is missing.
// The other parameters are optional and may be used to override specifications from the file:
//
//	-name=NAME will use NAME as a custom string that is written in the benchmark output.
//	We recommend including the commit-hash and/or something to identify the machine the benchmark was run on.
//	-jsonout=OUTFILE will cause output in JSON-format to be written to the specified file.
//	Note that if -jsonout=OUTFILE is provided, we will overwrite the file if it already exists, ignoring the overwrite flag in INPUTFILE.
//	-csvout=OUTFILE2 will cause output in csv-format to be written to the specified file. The same considerations for overwriting files apply as for JSON output.
//	Note that what actually gets written into the csv file depends on the JSON config.
//	-csvmetric=METRIC will cause the csv-output to include a table for METRIC. This is only meaningful if csv output is requested.
//
// We note that all of the latter parameters override existing settings in the JSON output.
// In particular, INPUTFILE can specify where to write output to, but this is not recommended.
//
// Note that the JSON output file OUTFILE contains the input settings (excluding jsonout, csvout that were provided via command-line flags) and can be used as INPUTFILE for another benchmark.
// In this case, the new benchmark will use the same settings and include the difference to the previous one.
func TestBenchmarkModExp(t *testing.T) {

	// Only run this (expensive) benchmark if explicitly requested.
	if *benchModExpFlag == "" {
		return
	}

	// parse INPUTFILE
	inputFileContents, err := os.ReadFile(*benchModExpFlag)
	if err != nil {
		t.Fatalf("failed to open file %v.\nError was %v", *benchModExpFlag, err)
	}
	var inputParams ModExpBenchInput
	err = json.Unmarshal(inputFileContents, &inputParams)
	if err != nil {
		t.Fatalf("failed to deserialize JSON from file %v.\nError was %v", *benchModExpFlag, err)
	}

	if benchnameFlag != nil && *benchnameFlag != "" {
		inputParams.Name = *benchnameFlag
	}

	// determine actual randomness to be used.
	var rnd *rand.Rand
	if inputParams.RndSeed != nil {
		rnd = rand.New(rand.NewSource(*inputParams.RndSeed))
	} else {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	// actually run the benchmarks now. We do this before parsing (and possibly validating) desired output parameters.
	// This is so we might actually output something (useful) even in case of some unexpected failure.
	out := inputParams.RunBenchmarks(rnd)

	// If requested, print some results to stdout via t.Log()
	if inputParams.DisplayResults {
		for _, res := range out.Results {
			t.Log(res.BenchmarkResult)
		}
	}

	// handle the csvMetric flag. We want this to appear in the JSON output (even though it does nothing).
	if csvMetric != nil && *csvMetric != "" {
		out.CSVTableForMetrics = []string{*csvMetric}
	}

	// determine whether and where to write JSON output.
	var (
		writeJSON       bool
		JSONoutfileName string
		overwriteJSON   bool
	)
	if jsonOutFlag != nil && *jsonOutFlag != "" { // command-line arg takes precendence
		writeJSON = true
		JSONoutfileName = *jsonOutFlag
		overwriteJSON = true
	} else if inputParams.JSONOut != "" {
		writeJSON = true
		JSONoutfileName = inputParams.JSONOut
		overwriteJSON = inputParams.JSONOutOverwrite
	}

	// actually write JSON output, if requested.
	if writeJSON {
		// write to []byte
		jsonOutputStream, err := json.MarshalIndent(out, "", "\t")
		if err != nil {
			t.Fatalf("error when JSON-serializing the benchmark output: %v", err)
		}

		var JSONoutfile *os.File
		if overwriteJSON {
			JSONoutfile, err = os.Create(JSONoutfileName)
			if err != nil {
				t.Fatalf("error when creating file with name %v for JSON output:\n%v", JSONoutfileName, err)
			}
		} else {
			JSONoutfile, err = os.OpenFile(JSONoutfileName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				if errors.Is(err, fs.ErrExist) {
					JSONoutfile, err = os.CreateTemp(".", JSONoutfileName+"-*")
					if err != nil {
						t.Fatalf("error when creating JSON output file:%v", err)
					}
					t.Logf("JSON output file %v already exists.\nWriting to %v instead", JSONoutfileName, JSONoutfile.Name())
				} else { // error other than already existing file
					t.Fatalf("error when creating JSON output file:%v", err)
				}
			}
		}
		defer JSONoutfile.Close()
		_, err = JSONoutfile.Write(jsonOutputStream)
		if err != nil {
			t.Fatalf("error when writing JSON output to file:%v", err)
		}
	}

	// determine whether and where to write CSV output.
	var (
		writeCSV       bool
		CSVoutfileName string
		overwriteCSV   bool
	)
	if csvOutFlag != nil && *csvOutFlag != "" {
		writeCSV = true
		CSVoutfileName = *csvOutFlag
	} else if inputParams.CSVOut != "" {
		writeCSV = true
		CSVoutfileName = inputParams.CSVOut
		overwriteCSV = inputParams.CSVOutOverwrite
	}

	// abort and alert user if there is nothing to output.
	if writeCSV && !out.CSVAll && !out.CSVMeta && len(out.CSVTableForMetrics) == 0 {
		t.Logf("Requested to output CSV to file %v, but none of the output options (table for metrics, all results, metadata) was set. Skipping output", CSVoutfileName)
		writeCSV = false
	}

	if writeCSV {
		var CSVoutfile *os.File
		var err error
		var writeLineSeparator bool // to write \n as separator in case we request several types of output
		if overwriteCSV {
			CSVoutfile, err = os.Create(CSVoutfileName)
			if err != nil {
				t.Fatalf("error when creating file with name %v for CSV output:\n%v", CSVoutfileName, err)
			}
		} else {
			CSVoutfile, err = os.OpenFile(CSVoutfileName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				if errors.Is(err, fs.ErrExist) {
					CSVoutfile, err = os.CreateTemp(".", CSVoutfileName+"-*")
					if err != nil {
						t.Fatalf("error when creating CSV output file:%v", err)
					}
					t.Logf("CSV output file %v already exsists.\nWriting to %v instead", CSVoutfileName, CSVoutfile.Name())
				} else { // error other than already existing file
					t.Fatalf("error when creating CSV output file:%v", err)
				}
			}
		}
		defer CSVoutfile.Close()
		if out.CSVMeta {
			err = out.WriteMetaAsCSV(CSVoutfile)
			if err != nil {
				t.Fatalf("error when write metadata to CSV:%v", err)
			}
			writeLineSeparator = true
		}

		for _, metric := range out.CSVTableForMetrics {
			if writeLineSeparator {
				_, err = CSVoutfile.WriteString("\n")
			}
			if err != nil {
				t.Fatalf("error when writing to CSV file:\n%v", err)
			}
			err = out.WriteCSVTable(CSVoutfile, metric)
			if err != nil {
				t.Fatalf("error when writing metric %v to CSV file:\n%v", metric, err)
			}
			writeLineSeparator = true
		}

		if out.CSVAll {
			if writeLineSeparator {
				_, err = CSVoutfile.WriteString("\n")
			}
			if err != nil {
				t.Fatalf("error when writing to CSV file:\n%v", err)
			}
			err = out.OutputAsCSV(CSVoutfile)
			if err != nil {
				t.Fatalf("error when writing outputs to CSV file:\n%v", err)
			}
			writeLineSeparator = true // does nothing.
		}
	}
}
