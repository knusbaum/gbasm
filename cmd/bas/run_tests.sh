#!/usr/bin/env bash

make clean
make bas bld

./bas puts_linux.bs string.bs >/dev/null 2>&1

#set -e
#set -x
rm tests/*.bs.o tests/*.out tests/*.stdout

fail=''
for t in `ls tests/*_test.bs`; do
    echo -e "\n\n############################ $t ############################"

    if [[ $t == *_err_test.bs ]]; then
        # Error test: assembler must reject this input.
        ./bas init_linux.bs $t >${t}.bas.out 2>&1
        if [[ $? == 0 ]]; then
            echo "Expected assembler to fail but it succeeded:"
            cat ${t}.bas.out
            echo ${t} FAIL
            fail=$(echo -e "$fail\n${t} FAIL")
            continue
        fi
        if [[ -f "${t}.expected" ]]; then
            diff -u "${t}.expected" "${t}.bas.out"
            if [[ $? != 0 ]]; then
                echo ${t} FAIL
                fail=$(echo -e "$fail\n${t} FAIL")
                continue
            fi
        fi
        echo ${t} PASS
        continue
    fi

    ./bas init_linux.bs $t >${t}.bas.out 2>&1
    if [[ $? != 0 ]]; then
		echo assembler failed for ${t}:
		cat ${t}.bas.out
		echo ${t} FAIL
		fail=$(echo -e "$fail\n${t} FAIL")
		continue
    fi
    # cat ${t}.bas.out
    ./bld -o ${t}.o main.bo string.bo >${t}.bld.out 2>&1
    if [[ $? != 0 ]]; then
		echo linker failed for ${t}:
		cat ${t}.bld.out
		echo ${t} FAIL
		fail=$(echo -e "$fail\n${t} FAIL")
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
		fail=$(echo -e "$fail\n${t} FAIL")
		continue
    fi
    if [[ -f "${t}.expected" ]]; then
		echo "Comparing output."
		diff -u "${t}.expected" "${t}.stdout"
		if [[ $? != 0 ]]; then
			echo ${t} FAIL
			fail=$(echo -e "$fail\n${t} FAIL")
			continue
		fi
    fi
    echo ${t} PASS
done


if [[ $fail != '' ]]; then
	echo -e '\nSUITE FAILED:'
	echo -e "${fail}"
	exit 1
fi

rm tests/*.bs.o tests/*.out tests/*.stdout
rm bas bld main.bo string.bo
echo "SUITE PASSED"
exit 0
