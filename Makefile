test: go_test bas_test bosc_test

go_test:
	go test -count 1 ./...

bas_test:
	cd ./cmd/bas && ./run_tests.sh

bosc_test:
	cd ./cmd/bosc && ./run_tests.sh

sloc:
	@echo "Source Lines of Code:"
	@find . -iname '*.go' -or -iname '*.sh' -or -iname '*.bos' -or -iname '*.bs' | xargs -n 1 cat | grep -v '^$$' | sed 's|^\s*||g' | grep -v '^//' | wc -l

loc:
	@echo "Lines of Code:"
	@find . -iname '*.go' -or -iname '*.sh' -or -iname '*.bos' -or -iname '*.bs' | xargs -n 1 cat | wc -l
