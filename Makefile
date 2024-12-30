test:
	go test -v ./...
	echo ${PWD}
	cd ./cmd/bas && ./run_tests.sh
	cd ./cmd/bosc && ./run_tests.sh
