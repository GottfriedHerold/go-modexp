#/usr/bin/bash
ORIGINAL_PATH=`pwd`
cd ..
CURRENT_BRANCH=`git rev-parse HEAD`
BASE_CONFIG="modexp_bench_config.json"
OUTNAME="BENCHMARKS/BENCHRUN_${CURRENT_BRANCH}"
CSVMETRICARG=""
while getopts ":i:o:" opt; do
	case ${opt} in
		i ) 
			BASE_CONFIG=$OPTARG
			CSVMETRICARGS="csvmetric=%Improvement(ns/op)"
			;;
		o)
			OUTNAME="BENCHMARKS/BENCHRUN_${OPTARG}"
			;;
		\? )
			echo "parse error"
			;;
		: )
			echo "missing argument"
			;;
	esac
done
JSONOUT="${OUTNAME}.json"
CSVOUT="${OUTNAME}.csv"
echo "Running benchmark, outputting results to ${OUTNAME}. Extra args = ${CSVMETRICARGS}"
go test -v -run=BenchmarkModExp -modexp=$ORIGINAL_PATH/$BASE_CONFIG -name=$CURRENT_BRANCH -jsonout=$ORIGINAL_PATH/$JSONOUT -csvout=$ORIGINAL_PATH/$CSVOUT $CSVMETRICARGS
cd $ORIGINAL_PATH
