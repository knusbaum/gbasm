FROM golang:latest AS build

WORKDIR /src
COPY . .

RUN go build -o /out/bplayd ./cmd/bplayd
RUN go build -o /out/bplay-runner ./cmd/bplay-runner
RUN go build -o /out/bosc ./cmd/bosc
RUN go build -o /out/bas ./cmd/bas
RUN go build -o /out/bld ./cmd/bld

RUN set -eux; \
    bundle=/out/playground; \
    mkdir -p "$bundle/objects" /tmp/bplay-build/builtin /tmp/bplay-build/io /tmp/bplay-build/fmt; \
    /out/bosc -importcfg=/dev/null -o /tmp/bplay-build/builtin/builtin.bs runtime/builtin/builtin.bos; \
    /out/bas -o "$bundle/objects/builtin.bo" /tmp/bplay-build/builtin/builtin.bs; \
    /out/bas -o "$bundle/objects/_init.bo" runtime/_init/init_linux.bs; \
    /out/bas -o "$bundle/objects/_heap.bo" runtime/_heap/heap_linux.bs; \
    /out/bas -o "$bundle/objects/_io_sys.bo" runtime/_io_sys/io_sys_linux.bs; \
    /out/bas -o "$bundle/objects/_iface.bo" runtime/_iface/iface_linux.bs; \
    /out/bas -o "$bundle/objects/string.bo" runtime/string/string.bs runtime/string/puts_linux.bs; \
    printf 'builtin=%s\nstring=%s\n_io_sys=%s\n' \
      "$bundle/objects/builtin.bo" \
      "$bundle/objects/string.bo" \
      "$bundle/objects/_io_sys.bo" \
      > /tmp/bplay-build/io/importcfg; \
    /out/bosc -importcfg=/tmp/bplay-build/io/importcfg -o /tmp/bplay-build/io/io.bs runtime/io/io.bos; \
    /out/bas -o "$bundle/objects/io.bo" /tmp/bplay-build/io/io.bs; \
    printf 'builtin=%s\nstring=%s\nio=%s\n' \
      "$bundle/objects/builtin.bo" \
      "$bundle/objects/string.bo" \
      "$bundle/objects/io.bo" \
      > /tmp/bplay-build/fmt/importcfg; \
    /out/bosc -importcfg=/tmp/bplay-build/fmt/importcfg -o /tmp/bplay-build/fmt/fmt.bs runtime/fmt/fmt.bos; \
    /out/bas -o "$bundle/objects/fmt.bo" /tmp/bplay-build/fmt/fmt.bs; \
    printf 'builtin=/usr/local/lib/boson/playground/objects/builtin.bo\nstring=/usr/local/lib/boson/playground/objects/string.bo\nio=/usr/local/lib/boson/playground/objects/io.bo\nfmt=/usr/local/lib/boson/playground/objects/fmt.bo\n' \
      > "$bundle/importcfg"

FROM golang:latest

COPY --from=build /out/bplayd /usr/local/bin/bplayd
COPY --from=build /out/bplay-runner /usr/local/bin/bplay-runner
COPY --from=build /out/bosc /usr/local/bin/bosc
COPY --from=build /out/bas /usr/local/bin/bas
COPY --from=build /out/bld /usr/local/bin/bld
COPY --from=build /out/playground /usr/local/lib/boson/playground
COPY --from=build /src/runtime /usr/local/lib/boson/runtime

EXPOSE 8086

CMD ["/usr/local/bin/bplayd", "-addr", ":8086", "-mode", "sandbox", "-runner", "/usr/local/bin/bplay-runner", "-toolchain-dir", "/usr/local/bin", "-runtime-bundle", "/usr/local/lib/boson/playground", "-docs-bosonpath", "/usr/local/lib/boson/runtime"]
