
b.out: main.bosc
	go build
	go build ../bas
	go build ../bld
	./bosc main.bosc
	./bas main.bs init_linux.bs
	./bld main.bo string.bo

clean:
	- rm main.bo main.bs b.out
