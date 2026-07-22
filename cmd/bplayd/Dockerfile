FROM golang:latest AS build

WORKDIR /src
COPY . .

RUN go build -o /out/bplayd ./cmd/bplayd
RUN go build -o /out/bplay-runner ./cmd/bplay-runner
RUN go build -o /out/bosc ./cmd/bosc
RUN go build -o /out/bas ./cmd/bas
RUN go build -o /out/bld ./cmd/bld

# Build the runtime bundle from source. The build tree lives at /out/playground
# but the image installs it at /usr/local/lib/boson/playground, so the bundle
# importcfg (which the server os.Stat's at startup) is written with that install
# prefix. All bundle logic lives in the shared script -- see the header there.
RUN bash scripts/build-runtime-bundle.sh /out /out/playground /usr/local/lib/boson/playground

FROM golang:latest

COPY --from=build /out/bplayd /usr/local/bin/bplayd
COPY --from=build /out/bplay-runner /usr/local/bin/bplay-runner
COPY --from=build /out/bosc /usr/local/bin/bosc
COPY --from=build /out/bas /usr/local/bin/bas
COPY --from=build /out/bld /usr/local/bin/bld
COPY --from=build /out/playground /usr/local/lib/boson/playground
# Runtime sources for the bdoc documentation server (-docs-bosonpath).
COPY --from=build /src/runtime /usr/local/lib/boson/runtime

EXPOSE 8086

CMD ["/usr/local/bin/bplayd", "-addr", ":8086", "-mode", "sandbox", "-runner", "/usr/local/bin/bplay-runner", "-toolchain-dir", "/usr/local/bin", "-runtime-bundle", "/usr/local/lib/boson/playground", "-docs-bosonpath", "/usr/local/lib/boson/runtime"]
