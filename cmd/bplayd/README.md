# bplayd Container Deployment Notes

This directory builds the Boson playground service image:

```text
ghcr.io/knusbaum/bplayd:<version>
```

The image contains:

- `/usr/local/bin/bplayd`
- `/usr/local/bin/bplay-runner`
- `/usr/local/bin/bosc`
- `/usr/local/bin/bas`
- `/usr/local/bin/bld`
- `/usr/local/lib/boson/playground/importcfg`
- `/usr/local/lib/boson/playground/objects/*.bo`

## Build and Publish

The local deployment convention matches the other services in this infra:

```sh
make -C cmd/bplayd version
make -C cmd/bplayd dev
make -C cmd/bplayd release
```

`dev` writes the pushed development tag to:

```text
/tmp/bplayd.devversion
```

`release` requires `HEAD` to be exactly on a git tag and the worktree to be
clean.

## Default Container Command

The image defaults to secure mode:

```sh
/usr/local/bin/bplayd \
  -addr :8086 \
  -mode sandbox \
  -runner /usr/local/bin/bplay-runner \
  -toolchain-dir /usr/local/bin \
  -runtime-bundle /usr/local/lib/boson/playground
```

The service listens on port `8086`.

HTTP routes:

- `/` and `/play` serve the playground UI.
- `/api/run` compiles/runs a submitted program.
- `/api/toolchain` reports bundled toolchain/runtime metadata.
- `/healthz` and `/readyz` are health checks.

## Sandbox Requirements

`-mode=sandbox` runs every compiler/link/run stage through `bplay-runner`.
The runner executes one command per invocation and requests these Linux
namespaces for the child command:

- user namespace
- PID namespace
- network namespace
- IPC namespace
- UTS namespace

This means the Nomad/Podman task must allow the container process to create
those namespaces. If the runtime denies unprivileged user namespaces or nested
namespaces, `/api/run` will fail at the stage that cannot start.

The current sandbox does **not** yet create a private mount namespace or
seccomp profile. Filesystem isolation and syscall filtering are future
hardening layers.

## Cgroups

`bplayd` supports an optional cgroup v2 root:

```sh
-cgroup-root /sys/fs/cgroup/bplayd
```

When provided, `bplayd` passes it to `bplay-runner`, and each stage gets its
own child cgroup. The runner writes:

- `memory.max`
- `pids.max`
- `cgroup.procs`

On timeout, the runner tries `cgroup.kill` and also falls back to process
group kill. It reads `memory.events` and `pids.events` to classify OOM or PID
exhaustion.

The cgroup root must be a writable/delegated cgroup v2 subtree from the
container's point of view. A plain read-only `/sys/fs/cgroup` is not enough.

Default limits when `-cgroup-root` is set:

| Stage | Memory | Pids | CPU rlimit | File size | Open files |
| --- | ---: | ---: | ---: | ---: | ---: |
| compile/link (`bosc`, `bas`, `bld`) | 256 MiB | 16 | 3 s | 16 MiB | 64 |
| program run | 64 MiB | 8 | 2 s | 1 MiB | 32 |

Without `-cgroup-root`, memory and pid cgroup limits are not active. The
runner still applies wall-clock timeout, output caps, process-group kill,
`RLIMIT_CPU`, `RLIMIT_FSIZE`, and `RLIMIT_NOFILE`.

## Insecure Local Override

For a deployment where nested namespaces are not ready yet, explicitly
override the command to use runner mode:

```sh
/usr/local/bin/bplayd \
  -addr :8086 \
  -mode runner \
  -runner /usr/local/bin/bplay-runner \
  -toolchain-dir /usr/local/bin \
  -runtime-bundle /usr/local/lib/boson/playground
```

This keeps the one-command-per-runner process boundary and rlimits, but it
does not request user/PID/network/IPC/UTS namespaces. Treat this as a
temporary compatibility mode, not the desired public setting.

## Nomad Planning Notes

A Nomad job should:

- use the `podman` driver;
- expose container port `8086` as an HTTP service;
- set the image to `ghcr.io/knusbaum/bplayd:${var.version}`;
- keep the default command for secure mode unless the host cannot support the
  namespace requirements;
- add `args` only when overriding defaults, for example to set
  `-cgroup-root`;
- provide a writable delegated cgroup v2 subtree if enabling cgroup limits;
- route with Traefik to the desired playground hostname;
- use `/readyz` for readiness if the deployment supports HTTP checks.

The image is otherwise stateless. Submitted snippets are not persisted yet.
