

all: bas bld out.o

asm: _bld _bas

clean:
	rm *.o *.bo bas bld

string.bo: puts_linux.bs string.bs
	./bas puts_linux.bs string.bs

main.bo: main.bs main3.bs init_linux.bs
	./bas main.bs main3.bs init_linux.bs

out.o : main.bo string.bo
	./bld main.bo string.bo

bld:
	go build ../bld

bas:
	go build

_bld:
	go build ../bld

_bas:
	go build

