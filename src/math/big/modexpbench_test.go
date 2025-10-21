package big

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
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
	jsonOutFlag   = flag.String("jsonout", "", "output ModExp benchmarks into this json file")
	benchnameFlag = flag.String("name", "", "name of benchmark, e.g.")
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
// Currently, we perform no randomization of base/exponent/modulus within a given test case
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
		b.ReportMetric(float64(testCase.ModulusBitLength), "bitlen(modulus)")
		b.ReportMetric(float64(testCase.ExponentBitLength), "bitlen(exponent)")
		if testCase.BaseBitLength != nil {
			b.ReportMetric(float64(*testCase.BaseBitLength), "bitlen(base)")
		}
		if testCase.ModulusTrailingZeros != nil {
			b.ReportMetric(float64(*testCase.ModulusTrailingZeros), "trailingZeros(modulus)")
		}
	}
}

// ModExpBenchInput is used to collect the data that we need to collect the input to a benchmarking request.
type ModExpBenchInput struct {
	ResetMemory bool // wipe the stack after each invocation.

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
	FurtherTestCases     []ModExpBenchTestCase

	// Metrics defines a list of additional CostMetrix to collect.
	// For each *CostMetric, we additionally collect some cost function Cost(testcase)
	// and we may add cost and time/cost to the extra output of the benchmark.
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

	Results []Result // When struct-embedded in ModExpBenchOutput, this holds the result of our benchmarks.
	// We define it in ModExpBenchInput (rather than output), because we want deserialize into it; in this case, it is used to
	// initialize oldResults, which is used for relative benchmarking.

	// When non-empty, Name defines a custom name that is printed and included in the output. Can be overwritten by command-line arg "name"
	// This can be used to record the context of the benchmark. We recommend including the hash of the commit that the benchmark was run on here.
	// Also, consider including something that identifies (the spec of) the machine the benchmark was run on.
	Name string

	RndSeed *int64 // optional RND seed. If nil, we derive from the current time.

	oldResults []Result `json:"-"`

	DisplayResults   bool   // whether we output results to stdout
	JSONOut          string // if set to a non-empty string, we write JSON to this filename. Can be overwritten by command-line arg
	JSONOutOverwrite bool   // if set, we overwrite existing files. Otherwise, we append -N to create a new filename.
}

type Result struct {
	ModExpBenchTestCase
	testing.BenchmarkResult
}

type ModExpBenchOutput struct {
	ModExpBenchInput
	StartTime time.Time
	EndTime   time.Time
}

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
// matching the restrictions of [(*testing.B).ReportMetric]
//
// JSONString is the string output when JSON-serializing a CostMetric. To
// deserialze from the string, you must call [RegisterCostMetric]
//
// We expect costMetrics to be defined as immutable global variables as
// var _ *CostMetric = (&CostMetric{...}).RegisterCostMetric()
type CostMetric struct {
	JSONString string                             // how this should be serialized as a string
	Cost       func(*ModExpBenchTestCase) float64 // Cost function to compute cost from the testcase parameters.
	CostName   *string                            // unit to display for Cost(testcase). If nil, do not display
	MetricName *string                            // unit to display for time / Cost(testcase). If nil, do not display.
}

// We hold a global map JSONString -> *CostMetric used to (de)serialization.
// This map is populated when we define *CostMetrics via var _ = (&CostMetric{...}).RegisterCostMetric()
var (
	registeredCostMetrics map[string]*CostMetric = make(map[string]*CostMetric)
	costMetricMutex       sync.Mutex
)

// RegisterCostMetric registers the given cost metric for JSON-deserialization, so the deserializer registers the json-string.
// This needs to be called (at least) once for every *CostMetric. It returns the receiver.
//
// We require that the metric.JSONString values for every registerd metric are non-empty and distinct, otherwise this function panics.
// Registering the same CostMetric twice works (and is a no-op), but has to use a pointer to the same object (rather than to a copy).
//
// This is intenteded to be called on (global) *CostMetrics on definition via
// var _ *CostMetric = (&CostMetric{...}).RegisterCostMetric()
//
// If metric is invalid, this function panics.
func (metric *CostMetric) RegisterCostMetric() *CostMetric {

	jsonName := metric.JSONString

	// Note: This function panics rather than reporting an error.
	// Since this is intended to be run on a set of global variables during variable initialization,
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
	for _, testCase := range z.FurtherTestCases {
		result := testing.Benchmark(testCase.makeBenchmark(rnd, z))
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

func TestBenchmarkModExp(t *testing.T) {
	if *benchModExpFlag == "" {
		return
	}
	inputFileContents, err := os.ReadFile(*benchModExpFlag)
	if err != nil {
		t.Fatalf("failed to open file %v.\nError was %v", *benchModExpFlag, err)
	}
	var inputParams ModExpBenchInput
	err = json.Unmarshal(inputFileContents, &inputParams)
	if err != nil {
		t.Fatalf("failed to deserialize JSON from file %v.\nError was %v", *benchModExpFlag, err)
	}
	var rnd *rand.Rand
	if inputParams.RndSeed != nil {
		rnd = rand.New(rand.NewSource(*inputParams.RndSeed))
	} else {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	out := inputParams.RunBenchmarks(rnd)

	if inputParams.DisplayResults {
		for _, res := range out.Results {
			t.Log(res.BenchmarkResult)
		}
	}

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

	if writeJSON {
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
}
