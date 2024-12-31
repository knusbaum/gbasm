test: go_test bas_test bosc_test

go_test:
	go test -count 1 ./...

bas_test:
	cd ./cmd/bas && ./run_tests.sh

bosc_test:
	cd ./cmd/bosc && ./run_tests.sh
