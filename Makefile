test:
	go test -count 1 ./...
	echo ${PWD}
	cd ./cmd/bas && ./run_tests.sh
	cd ./cmd/bosc && ./run_tests.sh
