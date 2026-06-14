FROM golang:latest AS build

WORKDIR /src
COPY . .

RUN go build -o /out/btourd ./cmd/btourd
RUN go build -o /out/bplay-runner ./cmd/bplay-runner
RUN go build -o /out/bosc ./cmd/bosc
RUN go build -o /out/bas ./cmd/bas
RUN go build -o /out/bld ./cmd/bld

RUN set -eux; \
    bundle=/out/playground; \
    mkdir -p "$bundle/objects" /tmp/btour-build/builtin /tmp/btour-build/io /tmp/btour-build/fmt; \
    /out/bosc -importcfg=/dev/null -o /tmp/btour-build/builtin/builtin.bs runtime/builtin/builtin.bos; \
    /out/bas -o "$bundle/objects/builtin.bo" /tmp/btour-build/builtin/builtin.bs; \
    /out/bas -o "$bundle/objects/_init.bo" runtime/_init/init_linux.bs runtime/_init/print_linux.bs; \
    /out/bas -o "$bundle/objects/_heap.bo" runtime/_heap/heap_linux.bs; \
    /out/bas -o "$bundle/objects/_io_sys.bo" runtime/_io_sys/io_sys_linux.bs; \
    /out/bas -o "$bundle/objects/_iface.bo" runtime/_iface/iface_linux.bs; \
    printf 'builtin=%s\n_io_sys=%s\n' \
      "$bundle/objects/builtin.bo" \
      "$bundle/objects/_io_sys.bo" \
      > /tmp/btour-build/io/importcfg; \
    /out/bosc -importcfg=/tmp/btour-build/io/importcfg -o /tmp/btour-build/io/io.bs runtime/io/io.bos; \
    /out/bas -o "$bundle/objects/io.bo" /tmp/btour-build/io/io.bs; \
    printf 'builtin=%s\nio=%s\n' \
      "$bundle/objects/builtin.bo" \
      "$bundle/objects/io.bo" \
      > /tmp/btour-build/fmt/importcfg; \
    /out/bosc -importcfg=/tmp/btour-build/fmt/importcfg -o /tmp/btour-build/fmt/fmt.bs runtime/fmt/fmt.bos; \
    /out/bas -o "$bundle/objects/fmt.bo" /tmp/btour-build/fmt/fmt.bs; \
    printf 'builtin=/usr/local/lib/boson/playground/objects/builtin.bo\nio=/usr/local/lib/boson/playground/objects/io.bo\nfmt=/usr/local/lib/boson/playground/objects/fmt.bo\n' \
      > "$bundle/importcfg"

FROM golang:latest

COPY --from=build /out/btourd /usr/local/bin/btourd
COPY --from=build /out/bplay-runner /usr/local/bin/bplay-runner
COPY --from=build /out/bosc /usr/local/bin/bosc
COPY --from=build /out/bas /usr/local/bin/bas
COPY --from=build /out/bld /usr/local/bin/bld
COPY --from=build /out/playground /usr/local/lib/boson/playground

EXPOSE 8087

CMD ["/usr/local/bin/btourd", "-addr", ":8087", "-mode", "sandbox", "-runner", "/usr/local/bin/bplay-runner", "-toolchain-dir", "/usr/local/bin", "-runtime-bundle", "/usr/local/lib/boson/playground"]
