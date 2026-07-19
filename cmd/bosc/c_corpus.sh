#!/bin/bash
# c_corpus.sh — measure the C transpiler backend against the .bos test corpus.
#
# Transpiles every non-_err corpus test with `-target=c`, compiles the emitted C
# with cc, runs it, and compares stdout + exit code against the test's
# `.expected` — the same ground truth the .bs backend matches, so this is
# differential testing between the two backends. Categories:
#
#   PASS — transpiled, compiled, ran, output matched.
#   SKIP — emitC hit a "not yet supported" feature gap. Expected; shrinks as
#          emitC grows. These are NOT failures.
#   FAIL — a real defect: emitC emitted uncompilable or wrong-output C, or the
#          oracle rejected a valid program. **FAIL must stay 0** — it is the
#          soundness invariant (the backend never miscompiles what it accepts).
#
# It also prints the most common blockers (feature -> count of tests it stops
# first), which is the data-driven priority order for growing emitC.
#
# Run from cmd/bosc. Builds ./bosc and the runtime importcfg first.
set -u

cd "$(dirname "$0")" || exit 1
go build . || { echo "go build failed"; exit 1; }
mmk test.importcfg >/dev/null 2>&1 || { echo "mmk test.importcfg failed"; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
pass=0 skip=0 fail=0
: > "$work/blockers"; : > "$work/fails"

for t in tests/*_test.bos; do
	case "$t" in *_err_test.bos) continue ;; esac
	out=$(./bosc -target=c -importcfg=test.importcfg -o "$work/c.c" "$t" 2>&1)
	if [ $? -ne 0 ]; then
		if echo "$out" | grep -q "not yet supported"; then
			skip=$((skip + 1))
			echo "$out" | grep -oE 'phase 1\): .* not yet supported' | head -1 \
				| sed -E 's/phase 1\): //; s/ not yet supported//' >> "$work/blockers"
		else
			fail=$((fail + 1))
			echo "TRANSPILE $t :: $(echo "$out" | grep -iE 'fatal|panic' | head -1)" >> "$work/fails"
		fi
		continue
	fi
	if ! cc -std=c11 -O2 -w "$work/c.c" -o "$work/c.exe" 2>/dev/null; then
		fail=$((fail + 1)); echo "CC $t" >> "$work/fails"; continue
	fi
	args=""; [ -f "$t.args" ] && args=$(cat "$t.args")
	# shellcheck disable=SC2086
	"$work/c.exe" $args > "$work/c.out" 2>/dev/null; ec=$?
	exp=0; [ -f "$t.exit" ] && exp=$(cat "$t.exit")
	if [ -f "$t.expected" ]; then
		if diff -q "$t.expected" "$work/c.out" >/dev/null 2>&1 && [ "$ec" = "$exp" ]; then
			pass=$((pass + 1))
		else
			fail=$((fail + 1)); echo "OUTPUT $t" >> "$work/fails"
		fi
	else
		pass=$((pass + 1))
	fi
done

echo "=== C-backend corpus sweep: $((pass + skip + fail)) non-err tests ==="
echo "PASS=$pass  SKIP(unsupported)=$skip  FAIL(bug)=$fail"
echo
echo "=== top blockers (feature -> tests it stops first) ==="
sort "$work/blockers" | uniq -c | sort -rn | head -25
if [ "$fail" -ne 0 ]; then
	echo
	echo "=== FAILs (must be 0) ==="
	cat "$work/fails"
fi
[ "$fail" -eq 0 ]
