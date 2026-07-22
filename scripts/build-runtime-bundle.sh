#!/usr/bin/env bash
# Build the playground/tour runtime bundle from source.
#
# This is the SINGLE source of truth for the bundle the bplayd/btourd Docker
# images ship. Both Dockerfiles call it, so a runtime-package change is a
# one-file edit here instead of drifting across two Dockerfiles (which is
# exactly how they broke: they still referenced heap_linux.bs / io_sys_linux.bs
# / iface_linux.bs after those became Boson packages).
#
# The set of objects and which are user-importable is defined authoritatively
# in internal/bpipeline/pipeline.go (runtimeObjects). Keep this in step with it;
# ValidateRuntimeBundle enforces that the bundle importcfg lists exactly the
# importable packages (builtin, io, fmt) and that every runtime object exists.
#
# Usage: build-runtime-bundle.sh <toolchain-bin-dir> <bundle-out-dir> [<install-prefix>]
#   toolchain-bin-dir  directory containing bosc, bas, bld
#   bundle-out-dir     where to write objects/ and importcfg
#   install-prefix     path the bundle will live at when the server runs, used
#                      for the importcfg entries (default: bundle-out-dir). The
#                      bundle importcfg must name paths valid at server start.
#
# Run from the repository root so runtime/*.bos paths resolve.
set -euo pipefail

TOOLS=$1
BUNDLE=$2
PREFIX=${3:-$2}

BOSC=$TOOLS/bosc
BAS=$TOOLS/bas
OBJ=$BUNDLE/objects
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$OBJ"

# compile <pkg> <src.bos> <importcfg-lines...>
# Compiles a Boson package to $OBJ/<pkg>.bo. importcfg lines are name=path
# entries for its imports, pointing at already-built objects in $OBJ.
compile() {
    local pkg=$1 src=$2; shift 2
    local cfg="$WORK/$pkg.importcfg"
    : > "$cfg"
    local line
    for line in "$@"; do printf '%s\n' "$line" >> "$cfg"; done
    "$BOSC" -importcfg="$cfg" -o "$WORK/$pkg.bs" "$src"
    "$BAS" -o "$OBJ/$pkg.bo" "$WORK/$pkg.bs"
}

# --- leaves: no Boson imports ---------------------------------------------
"$BOSC" -importcfg=/dev/null -o "$WORK/builtin.bs" runtime/builtin/builtin.bos
"$BAS" -o "$OBJ/builtin.bo" "$WORK/builtin.bs"

"$BAS" -o "$OBJ/_sys.bo"  runtime/_sys/sys_linux.bs
"$BAS" -o "$OBJ/_init.bo" runtime/_init/init_linux.bs runtime/_init/print_linux.bs

# --- Boson runtime, in dependency order -----------------------------------
# sys wraps the _sys syscall floor.
compile sys runtime/sys/sys.bos \
    "builtin=$OBJ/builtin.bo" "_sys=$OBJ/_sys.bo"

# _heap (the allocator) imports sys.
compile _heap runtime/_heap/heap.bos \
    "builtin=$OBJ/builtin.bo" "sys=$OBJ/sys.bo" "_sys=$OBJ/_sys.bo"

# _iface (interface assertion) imports _heap.
compile _iface runtime/_iface/iface.bos \
    "builtin=$OBJ/builtin.bo" "_heap=$OBJ/_heap.bo"

# io (typed file IO) imports sys.
compile io runtime/io/io.bos \
    "builtin=$OBJ/builtin.bo" "sys=$OBJ/sys.bo" "_sys=$OBJ/_sys.bo"

# fmt imports io.
compile fmt runtime/fmt/fmt.bos \
    "builtin=$OBJ/builtin.bo" "io=$OBJ/io.bo"

# --- bundle importcfg: exactly the user-importable packages ---------------
# builtin is auto-imported; io and fmt are the public runtime API. Paths are
# the install-time locations, since the server os.Stat's them at startup.
{
    printf 'builtin=%s/objects/builtin.bo\n' "$PREFIX"
    printf 'io=%s/objects/io.bo\n' "$PREFIX"
    printf 'fmt=%s/objects/fmt.bo\n' "$PREFIX"
} > "$BUNDLE/importcfg"

echo "runtime bundle built at $BUNDLE (importcfg -> $PREFIX)"
