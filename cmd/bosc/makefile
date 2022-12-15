

all: b.out


bosc:
	go build

bas:
	go build ../bas

bld:
	go build ../bld

bdump:
	go build ../bdump

main.bs: bosc main.bosc
	./bosc main.bosc

main.bo: bas main.bs init_linux.bs
	./bas main.bs init_linux.bs

string.bo: bas string.bs puts_linux.bs
	./bas string.bs puts_linux.bs

b.out: bld main.bo string.bo
	./bld main.bo string.bo

clean:
	- rm main.bo main.bs b.out 

nuke: clean
	- rm bosc bas bld