#!/usr/bin/env bash

make bosc bas bld
if [[ $? != 0 ]]; then
	echo "Failed to build toolchain."
	exit 1
fi

RUNTIME=../../runtime
rm -f string.bo init.bo
./bas -o string.bo $RUNTIME/string/puts_linux.bs $RUNTIME/string/string.bs >/dev/null 2>&1
./bas -o init.bo $RUNTIME/_init/init_linux.bs >/dev/null 2>&1

# Generate a project-wide importcfg.
cat > test.importcfg <<EOF
string=string.bo
EOF

#set -e
#set -x
rm tests/*.bos.o tests/*.bos.bo tests/*.bs tests/*.out tests/*.stdout

fail=''

for t in `ls tests/*_test.bos`; do
    echo -e "\n\n############################ $t ############################"

    if [[ $t == *_err_test.bos ]]; then
        # Error test: compiler must reject this input.
        ./bosc -importcfg=test.importcfg -o /dev/null $t >${t}.bosc.out 2>&1
        if [[ $? == 0 ]]; then
            echo "Expected compiler to fail but it succeeded:"
            cat ${t}.bosc.out
            echo ${t} FAIL
            fail=$(echo -e "$fail\n${t} FAIL")
            continue
        fi
        if [[ -f "${t}.expected" ]]; then
            # Strip "at file:line:col: " position prefix for stable comparison.
            sed 's/at [^:]*:[0-9]*:[0-9]*: //' ${t}.bosc.out > ${t}.bosc.stripped.out
            diff -u "${t}.expected" "${t}.bosc.stripped.out"
            if [[ $? != 0 ]]; then
                echo ${t} FAIL
                fail=$(echo -e "$fail\n${t} FAIL")
                continue
            fi
        fi
        echo ${t} PASS
        continue
    fi

    ./bosc -importcfg=test.importcfg -o ${t}.bs $t >${t}.bosc.out 2>&1
    if [[ $? != 0 ]]; then
		echo compiler failed for ${t}:
		cat ${t}.bosc.out
		echo ${t} FAIL
		fail=$(echo -e "$fail\n${t} FAIL")
		continue
    fi
    ./bas -o ${t}.bo ${t}.bs >${t}.bas.out 2>&1
    if [[ $? != 0 ]]; then
		echo assembler failed for ${t}:
		cat ${t}.bas.out
		echo ${t} FAIL
		fail=$(echo -e "$fail\n${t} FAIL")
		continue
    fi
    # cat ${t}.bas.out
    ./bld -o ${t}.o ${t}.bo string.bo init.bo >${t}.bld.out 2>&1
    if [[ $? != 0 ]]; then
		echo linker failed for ${t}:
		cat ${t}.bld.out
		echo ${t} FAIL
		fail=$(echo -e "$fail\n${t} FAIL")
		continue
    fi
    # If the test has a matching .args file, pass its contents as
    # process arguments. Otherwise invoke with no arguments.
    if [[ -f "${t}.args" ]]; then
        # shellcheck disable=SC2046 — intentional word splitting on argv tokens.
        ${t}.o $(cat ${t}.args) > ${t}.stdout
    else
        ${t}.o > ${t}.stdout
    fi
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
		#echo "Comparing output."
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

rm tests/*.bos.o tests/*.bos.bo tests/*.bs tests/*.out tests/*.stdout
rm bosc bas bld string.bo init.bo test.importcfg
echo "SUITE PASSED"
exit 0
