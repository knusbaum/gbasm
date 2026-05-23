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
#   BOSON_HOME      root of the boson toolchain (defaults to dir of bosc)
#   BOSC, BAS, BLD  paths to toolchain binaries
#   BOSONPATH       colon-separated package search path
#                   (default: $BOSON_HOME/runtime:.)
#
# An import "foo/bar" in source code is resolved by walking BOSONPATH entries
# and selecting the first directory containing foo/bar/. That directory holds
# .bos and/or .bs files. The package's compiled artifact lives at
# target/foo/bar.bo.

BOSC=${BOSC:-bosc}
BAS=${BAS:-bas}
BLD=${BLD:-bld}
BOSON_HOME=${BOSON_HOME:-$(dirname $(which $BOSC))}
BDOC=${BDOC:-$BOSON_HOME/bdoc}
BOSONPATH=${BOSONPATH:-$BOSON_HOME/runtime:.}
BDOC_ADDR=${BDOC_ADDR:-:8686}

# ---- Helpers ---------------------------------------------------------------

# resolve_pkg <import-path>: prints the source directory for the package, by
# walking BOSONPATH and returning the first entry containing <import-path>/.
# Returns nonzero (with no output) if not found.
resolve_pkg() {
    local imp=$1
    local d
    local IFS=":"
    for d in $BOSONPATH; do
        if [ -d "$d/$imp" ]; then
            echo "$d/$imp"
            return 0
        fi
    done
    return 1
}

# pkg_sources <srcdir>: print all .bos and .bs files in the package's source
# directory, sorted for reproducibility.
pkg_sources() {
    local d=$1
    ls "$d"/*.bos "$d"/*.bs 2>/dev/null | sort
}

# pkg_import_targets <srcdir>: print target/<path>.bo for each import declared
# by .bos files in srcdir.
pkg_import_targets() {
    local d=$1
    local bos
    bos=$(ls "$d"/*.bos 2>/dev/null)
    [ -z "$bos" ] && return 0
    # Capture stdout+stderr so a bosc failure is reported rather than masked
    # by the pipe to sed (which would otherwise succeed on empty input and
    # silently drop bosc's exit code).
    local out
    if ! out=$($BOSC -listimports $bos 2>&1); then
        echo "boson.mmk: $BOSC -listimports failed in $d:" >&2
        echo "$out" >&2
        exit 1
    fi
    [ -z "$out" ] && return 0
    echo "$out" | sed 's|^|target/|; s|$|.bo|'
}

# write_importcfg <out-file> <dep>...: writes pkg=path lines for each dep that
# matches the target/<path>.bo pattern.
# pkg_resolve_and_deps <import-path>: resolve to srcdir via BOSONPATH, then
# print source files and import-targets. Fails if the package is not found.
# bos_exe_deps <source-dir>: print the deps for a bos_exe target — local
# source files, target/<path>.bo for each import, plus the runtime init .bo.
bos_exe_deps() {
    local d=$1
    pkg_sources "$d"
    pkg_import_targets "$d"
    echo "target/_init.bo"
}

pkg_resolve_and_deps() {
    local imp=$1
    local srcdir
    srcdir=$(resolve_pkg "$imp")
    if [ -z "$srcdir" ]; then
        echo "boson.mmk: cannot find package '$imp' in BOSONPATH=$BOSONPATH" >&2
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
    for src in "$srcdir"/*.bos; do
        [ -e "$src" ] || continue
        bs="$workdir/$(basename $src .bos).bs"
        $BOSC -importcfg=$cfg -o $bs "$src"
        asm_files+=("$bs")
    done
    for src in "$srcdir"/*.bs; do
        [ -e "$src" ] || continue
        asm_files+=("$src")
    done

    $BAS -o "$outbo" "${asm_files[@]}"

    rm -f $cfg
}

# ---- bos_pkg ---------------------------------------------------------------
# Pattern rule for building target/<import-path>.bo. The import path is the
# pattern capture group; the source directory is found via BOSONPATH.

deftype bos_pkg {
    stat -c %Y "$target" 2>/dev/null || return 1
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
    rm -f "$target"
    rm -rf "${target%.bo}.work"
}

# ---- bos_exe ---------------------------------------------------------------
# A Boson executable. The user names a source directory; the body discovers
# imports, depends on each as target/<path>.bo, and links the whole thing
# together with the runtime init.

deftype bos_exe {
    stat -c %Y "$target" 2>/dev/null || return 1
}

defbody bos_exe : $(bos_exe_deps "$source") {
    # Exit on first failed command so a bosc/bas/bld error aborts the build
    # with a clean error rather than letting subsequent steps (chmod, etc.)
    # run on stale or missing artifacts.
    set -e
    # Build the executable's own package into a local .bo (not in target/,
    # since it isn't importable by name).
    workdir="$target.work"
    mkdir -p "$workdir"

    cfg=$(mktemp)
    trap 'rm -f $cfg' EXIT
    write_importcfg "$cfg" "${dep[@]}"

    asm_files=()
    for src in "$source"/*.bos; do
        [ -e "$src" ] || continue
        bs="$workdir/$(basename $src .bos).bs"
        $BOSC -importcfg=$cfg -o $bs "$src"
        asm_files+=("$bs")
    done
    for src in "$source"/*.bs; do
        [ -e "$src" ] || continue
        asm_files+=("$src")
    done

    mainbo="$workdir/main.bo"
    $BAS -o "$mainbo" "${asm_files[@]}"

    # Collect all package .bo files from the deps.
    link_files=("$mainbo")
    for d in "${dep[@]}"; do
        case "$d" in
            target/*.bo) link_files+=("$d") ;;
        esac
    done

    $BLD -o "$target" "${link_files[@]}"
    chmod +x "$target"
}

defbody bos_exe clean {
    rm -f "$target"
    rm -rf "$target.work"
}

# ---- docs ------------------------------------------------------------------
# `mmk docs` launches bdoc with the project's BOSONPATH so the documentation
# server can see every package on the search path — not only the ones this
# project's executables happen to import.

## Run the bdoc documentation server against the project's BOSONPATH.
[docs all] : {
    echo "BOSONPATH=$BOSONPATH"
    echo "Serving on http://localhost${BDOC_ADDR}/"
    BOSONPATH="$BOSONPATH" $BDOC -addr "$BDOC_ADDR"
}
