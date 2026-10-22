# boson.mmk — mmk types for building Boson packages and binaries.
#
# Usage:
#
#   include $BOSON_HOME/boson.mmk
#   bos_exe hello source=src
#
# That's it. The body discovers imports via `bosc -listimports`, locates each
# package's source via BOSONPATH, and builds a target/<import-path>.bo for it
# using the bos_pkg pattern rule. Sources for the executable's own package
# come from $source.
#
# Configuration (env vars):
#   BOSON_TARGET    linux/amd64 (default) or darwin/arm64
#   BOSON_HOME      root of the boson toolchain (defaults to dir of bosc)
#   BOSC, BAS, BLD  paths to toolchain binaries
#   BOSONPATH       colon-separated project package roots (default: .)
#                   $BOSON_HOME/runtime is always prepended automatically
#
# An import "foo/bar" is resolved by walking the runtime and BOSONPATH entries
# and selecting the first directory containing foo/bar/. That directory holds
# .bos and/or .bs files. The package's compiled artifact lives at
# target/foo/bar.bo.

BOSON_TARGET=${BOSON_TARGET:-linux/amd64}
BOSC=${BOSC:-bosc}
BAS=${BAS:-bas}
BLD=${BLD:-bld}
BOSON_HOME=${BOSON_HOME:-$(dirname $(which $BOSC))}
BDOC=${BDOC:-bdoc}
# Project roots extend the standard library search path.
__boson_search_path="$BOSON_HOME/runtime:${BOSONPATH:-.}"
BDOC_ADDR=${BDOC_ADDR:-:8686}

# ---- Helpers ---------------------------------------------------------------

# resolve_pkg <import-path>: prints the source directory for the package, by
# walking the effective search path and finding <import-path>/.
# Returns nonzero (with no output) if not found.
resolve_pkg() {
    local imp=$1
    local d
    local IFS=":"
    for d in $__boson_search_path; do
        if [ -d "$d/$imp" ]; then
            echo "$d/$imp"
            return 0
        fi
    done
    return 1
}

# pkg_sources <srcdir>: print all .bos and .bs files in the package's source
# directory, sorted for reproducibility.
# Platform-specific sources override the historical Linux assembly files.
# Generic .bos/.bs files are shared; *_darwin_arm64 and *_linux[_amd64]
# suffixes select the corresponding target.
pkg_sources() {
    local d=$1 src
    for src in "$d"/*.bos "$d"/*.bs; do
        [ -e "$src" ] || continue
        case "$src" in
            *_darwin_arm64.bos|*_darwin_arm64.bs)
                [ "$BOSON_TARGET" = darwin/arm64 ] || continue ;;
            *_linux.bos|*_linux.bs|*_linux_amd64.bos|*_linux_amd64.bs)
                [ "$BOSON_TARGET" = linux/amd64 ] || continue ;;
        esac
        echo "$src"
    done | sort
}

# pkg_import_targets <srcdir>: print target/<path>.bo for each import declared
# by .bos files in srcdir.
pkg_import_targets() {
    local d=$1
    local bos
    bos=$(pkg_sources "$d" | sed -n '/\.bos$/p')
    [ -z "$bos" ] && return 0
    # Capture stdout+stderr so a bosc failure is reported rather than masked
    # by the pipe to sed (which would otherwise succeed on empty input and
    # silently drop bosc's exit code).
    local out
    if ! out=$($BOSC -target="$BOSON_TARGET" -listimports $bos 2>&1); then
        echo "boson.mmk: $BOSC -target="$BOSON_TARGET" -listimports failed in $d:" >&2
        echo "$out" >&2
        exit 1
    fi
    [ -z "$out" ] && return 0
    echo "$out" | sed 's|^|target/|; s|$|.bo|'
}

# all_pkg_import_targets <srcdir>: BFS over the full transitive import closure
# starting from srcdir, printing target/<path>.bo for every reachable package.
# Packages whose source cannot be found in BOSONPATH are skipped (they may be
# pure-.bs packages with no .bos imports to discover).
#
# Uses file-based queue/seen sets to avoid bash associative arrays or array
# operations not supported by mmk's function-body parser.
all_pkg_import_targets() {
    local d="$1"
    local seen_file queue_file t imp srcdir
    seen_file=$(mktemp)
    queue_file=$(mktemp)

    # Seed queue with direct imports.
    pkg_import_targets "$d" > "$queue_file"
    cp "$queue_file" "$seen_file"

    # BFS: read from queue_file; appending new entries to it mid-loop causes
    # bash to deliver them on subsequent reads (fd stays open to the file).
    while IFS= read -r t; do
        echo "$t"
        imp="${t#target/}"
        imp="${imp%.bo}"
        srcdir=$(resolve_pkg "$imp") 2>/dev/null || continue
        while IFS= read -r new_t; do
            if ! grep -qxF "$new_t" "$seen_file" 2>/dev/null; then
                echo "$new_t" >> "$seen_file"
                echo "$new_t" >> "$queue_file"
            fi
        done < <(pkg_import_targets "$srcdir")
    done < "$queue_file"

    rm -f "$seen_file" "$queue_file"
}

# write_importcfg <out-file> <dep>...: writes pkg=path lines for each dep that
# matches the target/<path>.bo pattern.
# pkg_resolve_and_deps <import-path>: resolve to srcdir via BOSONPATH, then
# print source files and import-targets. Fails if the package is not found.
# bos_exe_deps <source-dir>: print the deps for a bos_exe target — local
# source files, target/<path>.bo for each import (transitively), plus the
# runtime init .bo.
bos_exe_deps() {
    local d=$1
    pkg_sources "$d"
    all_pkg_import_targets "$d"
    echo "target/_heap.bo"
    # _heap is Boson now and imports sys, so its transitive deps link too.
    echo "target/sys.bo"
    echo "target/_sys.bo"
    echo "target/_init.bo"
    # _iface backs interface-to-interface type assertions. It is not a
    # source-level import (the compiler emits a bare call), so it is always
    # linked; the linker's reachability drops it when unused.
    echo "target/_iface.bo"
}

pkg_resolve_and_deps() {
    local imp=$1
    local srcdir
    srcdir=$(resolve_pkg "$imp")
    if [ -z "$srcdir" ]; then
        echo "boson.mmk: cannot find package '$imp' in search path $__boson_search_path" >&2
        exit 1
    fi
    pkg_sources "$srcdir"
    pkg_import_targets "$srcdir"
}

write_importcfg() {
    local out=$1; shift
    true > "$out"
    local d pkg
    for d in "$@"; do
        case "$d" in
            target/*.bo)
                pkg="${d#target/}"
                pkg="${pkg%.bo}"
                echo "$pkg=$d" >> "$out"
                ;;
        esac
    done
}

# build_package <srcdir> <target.bo> <dep>...: compile .bos sources in srcdir,
# assemble all .bos and .bs files (after compilation) into target.bo.
# The deps are used to construct the importcfg.
build_package() {
    local srcdir=$1 outbo=$2; shift 2
    local workdir="${outbo%.bo}.work"
    mkdir -p "$(dirname $outbo)" "$workdir"

    local cfg
    cfg=$(mktemp)
    write_importcfg "$cfg" "$@"

    local asm_files=()
    local src bs
    local bos_files
    bos_files=()
    while IFS= read -r src; do
        case "$src" in *.bos) bos_files+=("$src") ;; esac
    done < <(pkg_sources "$srcdir")
    if [ "${#bos_files[@]}" -gt 0 ]; then
        bs="$workdir/package.bs"
        $BOSC -target="$BOSON_TARGET" -importcfg="$cfg" -o "$bs" "${bos_files[@]}"
        asm_files+=("$bs")
    fi
    while IFS= read -r src; do
        case "$src" in *.bs) asm_files+=("$src") ;; esac
    done < <(pkg_sources "$srcdir")

    $BAS -target="$BOSON_TARGET" -o "$outbo" "${asm_files[@]}"
    echo "$BOSON_TARGET" > "$outbo.target"

    rm -f $cfg
}

# ---- bos_pkg ---------------------------------------------------------------
# Pattern rule for building target/<import-path>.bo. The import path is the
# pattern capture group; the source directory is found via BOSONPATH.

deftype bos_pkg {
    [ "$(cat "$target.target" 2>/dev/null)" = "$BOSON_TARGET" ] || return 1
    stat -c %.Y "$target" 2>/dev/null || stat -f %m "$target" 2>/dev/null || return 1
}

bos_pkg 'target/(.*)\.bo' : $(pkg_resolve_and_deps "$1") {
    # Exit on first failed command so a bosc/bas error aborts the build with
    # a clean error rather than letting subsequent steps run on stale state.
    set -e
    imp="${target#target/}"
    imp="${imp%.bo}"
    srcdir=$(resolve_pkg "$imp")
    build_package "$srcdir" "$target" "${dep[@]}"
}

defbody bos_pkg clean {
    rm -f "$target" "$target.target"
    rm -rf "${target%.bo}.work"
}

# ---- bos_exe ---------------------------------------------------------------
# A Boson executable. The user names a source directory; the body discovers
# imports, depends on each as target/<path>.bo, and links the whole thing
# together with the runtime init.

deftype bos_exe source= {
    [ "$(cat "target/$target.target" 2>/dev/null)" = "$BOSON_TARGET" ] || return 1
    stat -c %.Y "target/$target" 2>/dev/null || stat -f %m "target/$target" 2>/dev/null || return 1
}

defbody bos_exe : $(bos_exe_deps "$source") {
    # Exit on first failed command so a bosc/bas/bld error aborts the build
    # with a clean error rather than letting subsequent steps (chmod, etc.)
    # run on stale or missing artifacts.
    set -e
    # All build artifacts (the exe itself, intermediate .bs/.bo) live under
    # target/. The exe's workdir is prefixed with `_exe_` so it can't collide
    # with a same-named package's workdir (bos_pkg writes target/<pkg>.work/).
    outexe="target/$target"
    workdir="target/_exe_$target.work"
    mkdir -p "$(dirname "$outexe")" "$workdir"

    cfg=$(mktemp)
    trap 'rm -f $cfg' EXIT
    write_importcfg "$cfg" "${dep[@]}"

    asm_files=()
    bos_files=()
    while IFS= read -r src; do
        case "$src" in *.bos) bos_files+=("$src") ;; esac
    done < <(pkg_sources "$source")
    if [ "${#bos_files[@]}" -gt 0 ]; then
        bs="$workdir/package.bs"
        $BOSC -target="$BOSON_TARGET" -importcfg="$cfg" -o "$bs" "${bos_files[@]}"
        asm_files+=("$bs")
    fi
    while IFS= read -r src; do
        case "$src" in *.bs) asm_files+=("$src") ;; esac
    done < <(pkg_sources "$source")

    mainbo="$workdir/main.bo"
    $BAS -target="$BOSON_TARGET" -o "$mainbo" "${asm_files[@]}"

    # Collect all package .bo files from the deps, de-duplicated. A package can
    # appear twice -- e.g. sys is both a hardcoded always-link dep (for _heap)
    # and discovered transitively when a program imports io -- and the linker
    # rejects duplicate packages.
    link_files=("$mainbo")
    seen=" "
    for d in "${dep[@]}"; do
        case "$d" in
            target/*.bo)
                case "$seen" in
                    *" $d "*) ;;
                    *) link_files+=("$d"); seen="$seen$d " ;;
                esac
                ;;
        esac
    done

    $BLD -target="$BOSON_TARGET" -o "$outexe" "${link_files[@]}"
    chmod +x "$outexe"
    echo "$BOSON_TARGET" > "$outexe.target"
}

defbody bos_exe clean {
    rm -f "target/$target" "target/$target.target"
    rm -rf "target/_exe_$target.work"
}

defbody bos_exe run : $target {
    target/$target
}

# ---- docs ------------------------------------------------------------------
# `mmk docs` launches bdoc with the project's BOSONPATH so the documentation
# server can see every package on the search path — not only the ones this
# project's executables happen to import.

## Run the bdoc documentation server against the project's BOSONPATH.
[docs all] : {
    echo "BOSONPATH=$__boson_search_path"
    echo "Serving on http://localhost${BDOC_ADDR}/"
    BOSONPATH="$__boson_search_path" $BDOC -addr "$BDOC_ADDR"
}
