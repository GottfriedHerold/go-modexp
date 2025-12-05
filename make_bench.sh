#/usr/bin/bash
CURRENT_BRANCH=`git rev-parse HEAD`
echo Running benchmark for branch $CURRENT_BRANCH
git checkout ConsistentBenchmark modexpbench_test.go
go test -benchmem -run=^TestCSV$  -timeout 0 -bench ^\$ math/big -v | tee BENCH-FOR-$CURRENT_BRANCH.csv
git checkout $CURRENT_BRACH modexpbench_test.go
