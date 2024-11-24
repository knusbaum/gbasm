#!/usr/bin/env bash

./bas puts_linux.bs string.bs >/dev/null 2>&1

#set -e
#set -x
rm tests/*.bs.o tests/*.out tests/*.stdout

for t in `ls tests/*_test.bs`; do
    echo -e "\n\n############################ $t ############################"
    ./bas init_linux.bs $t >${t}.bas.out 2>&1
    if [[ $? != 0 ]]; then
	echo assembler failed for ${t}:
	cat ${t}.bas.out
	echo ${t} FAIL
	continue
    fi
    # cat ${t}.bas.out
    ./bld -o ${t}.o main.bo string.bo >${t}.bld.out 2>&1
    if [[ $? != 0 ]]; then
	echo linker failed for ${t}:
	cat ${t}.bld.out
	echo ${t} FAIL
	continue
    fi
    ${t}.o > ${t}.stdout
    ecode="$?"
    if [[ "$ecode" != "0" ]]; then
	echo $t exited with $ecode
	echo -e 'stdout:\n```'
	cat ${t}.stdout
	echo '```'
	echo ${t} FAIL
	continue
    fi
    if [[ -f "${t}.expected" ]]; then
	echo "Comparing output."
	diff -u "${t}.expected" "${t}.stdout"
	if [[ $? != 0 ]]; then
	    echo ${t} FAIL
	    continue
	fi
    fi
    echo ${t} PASS
done


rm tests/*.bs.o tests/*.out tests/*.stdout
