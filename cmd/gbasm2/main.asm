.global start
.text
start:
	mov $0x2000004, %rax
	mov $1, %rdi
	leaq msg(%rip), %rsi
	leaq b(%rip), %rsi
	mov $14, %rdx
	syscall
.byte 0x90
	mov $10, %rdi
	movq $0x2000001, %rax
	syscall

.data
b:
.byte 10
msg:
.ascii "hello, world!\n"
