# boson.mmk — mmk types for building Boson packages and binaries.
#
# Include this from your project's mmkfile, then declare your packages:
#
#   include $BOSON_HOME/boson.mmk
#
#   bos_pkg string srcdir=runtime
#   bos_pkg main   srcdir=src : string
#
#   boson_exe myapp : main string
#
# A bos_pkg target produces <target>.bo by compiling all .bos files in
# srcdir with bosc, then assembling all .bs files (including those bosc
# emits) with bas. Its deps must list every package it imports.
#
# A boson_exe target produces <target> (an ELF executable) by linking
# its dep .bo files plus the runtime init code with bld.
#
# Required env vars:
#   BOSC     path to bosc binary
#   BAS      path to bas binary
#   BLD      path to bld binary
#   BOSON_RUNTIME  path to dir containing init_linux.bs (and any other
#                  runtime .bs files that need to be assembled into binaries)

BOSC=${BOSC:-bosc}
BAS=${BAS:-bas}
BLD=${BLD:-bld}
BOSON_RUNTIME=${BOSON_RUNTIME:-$(dirname $(which $BOSC))}

# A Boson package. The 'srcdir' option points at the directory containing
# the .bos source files (defaults to a directory named after the target).
# Deps are other bos_pkg targets — the packages this one imports.
deftype bos_pkg {
    stat -c %Y "$target.bo" 2>/dev/null || return 1
}

defbody bos_pkg {
    srcdir=${srcdir:-$target}
    # Build the importcfg from declared package deps. A dep is treated as a
    # package iff it has no file-extension suffix; source-file deps (used
    # only for mtime tracking) are skipped.
    cfg=$(mktemp)
    trap 'rm -f $cfg' EXIT
    for d in "${dep[@]}"; do
        case "$d" in
            *.bos|*.bs) ;;        # source files — skip
            *) echo "$d=$d.bo" >> $cfg ;;
        esac
    done
    # Find all .bos sources in the package's srcdir.
    sources=("$srcdir"/*.bos)
    # bosc emits a .bs file per source. Use a per-package work directory.
    workdir=$target.work
    mkdir -p $workdir
    asm_files=()
    for src in "${sources[@]}"; do
        bs=$workdir/$(basename "$src" .bos).bs
        $BOSC -importcfg=$cfg -o $bs "$src"
        asm_files+=("$bs")
    done
    # Assemble all .bs into a single .bo for the package.
    $BAS -o "$target.bo" "${asm_files[@]}"
}

defbody bos_pkg clean {
    rm -f "$target.bo"
    rm -rf "$target.work"
}

# A hand-written assembly-only package. Used for runtime libraries written
# directly in .bs (no Boson source). All .bs files in srcdir are assembled
# into a single <target>.bo. The .bs files' 'package' declarations must
# match the target name.
deftype bos_asmpkg {
    stat -c %Y "$target.bo" 2>/dev/null || return 1
}

defbody bos_asmpkg {
    srcdir=${srcdir:-$target}
    sources=("$srcdir"/*.bs)
    $BAS -o "$target.bo" "${sources[@]}"
}

defbody bos_asmpkg clean {
    rm -f "$target.bo"
}

# A Boson executable. Deps must list every package that contributes
# .bo files to the link. The runtime init code is added automatically.
deftype boson_exe {
    stat -c %Y "$target" 2>/dev/null || return 1
}

defbody boson_exe {
    # The runtime init code provides the ELF entry symbol 'start'.
    # Assemble it once per build into a known location.
    initbo=$BOSON_RUNTIME/init_linux.bo
    $BAS -o "$initbo" $BOSON_RUNTIME/init_linux.bs
    # Link all package .bo files plus the runtime init. Same convention as
    # bos_pkg: package deps have no extension; source-file deps are skipped.
    bo_files=("$initbo")
    for d in "${dep[@]}"; do
        case "$d" in
            *.bos|*.bs) ;;
            *) bo_files+=("$d.bo") ;;
        esac
    done
    $BLD -o "$target" "${bo_files[@]}"
    chmod +x "$target"
}

defbody boson_exe clean {
    rm -f "$target"
}
