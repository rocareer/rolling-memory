# Rolling Memory for DeepSeek Harness

This third-party DSH bundle exposes the Rolling Memory `recall` MCP tool and the optional `rolling-memory-dsh` skill. It uses the local `local_plain` vault; it does not download or send memory to a service. Current Rolling Memory builds synchronize your DSH session history into that local vault in the background; the earliest builds only counted sessions without importing them. If your agent uses a remote model, recalled text included in a request can be sent to that model provider.

## Requirements

- DSH `0.2.2-rc.3` and `pnpm` for plugin installation; `npm` is needed to build the tarball. Other DSH versions have not been verified.
- A compatible Rolling Memory executable named `rolling-memory` on the DSH process's `PATH`. The bundle is not pinned to a Rolling Memory version; install a current build separately and check `rolling-memory --version`. See the [Rolling Memory installation guide](https://github.com/rocareer/rolling-memory#quick-start) for installation options. This bundle does not install or update the executable. Graphical launchers may have a different `PATH` from your terminal; ensure the executable is available to the DSH process.
- A DSH profile with the standard tools and skill services, such as `web`, `headless`, or `sdk`. The `sdk-minimal` profile does not include the skill services by default.

## Build a local package

The directory is self-contained: it carries its own `LICENSE`, the bundle patch, the bundled skill and the packaging helpers. Build the distributable tarball with the packaging helper and an existing output directory:

```sh
bash examples/dsh-plugin/pack.sh /absolute/path/to/output-directory
```

The helper writes `rocareer-dsh-rolling-memory-0.0.16.tgz` to the output directory and refuses to overwrite an existing package. `npm pack` directly in this directory produces the same complete package. No registry access or install scripts are needed.

## Install and use

Before installing, inspect the target profile:

```sh
dsh --profile web --dump-config
```

DSH requires unique server names within a scope. If an existing Rolling Memory MCP entry uses `serverName: rolling-memory`, remove or disable that redundant entry using the method that created it before adding the bundle; while both are present, DSH reports a duplicate-serverName error for the later entry and the bundle's tool remains the one that loads. An older entry using a different server name can expose a second recall tool. Review the profile rather than removing unrelated configuration or memory data. Rolling Memory's own install or update may recreate a DSH entry, so repeat this check after those operations.

Then install a tarball and inspect the resulting configuration:

```sh
dsh plugin --profile web add --offline /absolute/path/to/rocareer-dsh-rolling-memory-0.0.16.tgz
dsh --profile web --dump-config
dsh --profile web
```

A published tarball URL works the same way, for example:

```sh
dsh plugin --profile web add https://github.com/rocareer/rolling-memory/releases/download/dsh-plugin-v0.0.16/rocareer-dsh-rolling-memory-0.0.16.tgz
```

To install from the public Git repository instead, pin the bundle's directory at a commit:

```sh
dsh plugin --profile web add 'git+https://github.com/rocareer/rolling-memory.git#<commit>&path:/examples/dsh-plugin'
```

pnpm requires one-time approval before running a git-hosted plugin's build scripts (the bundle's `prepack` license check): the first attempt fails and DSH prints the exact `allowBuilds` key to add to the profile's `pnpm-workspace.yaml`; add it and re-run.

Replace `web` with the profile you use. Offline installation depends on the local tarball and any other dependencies already available to pnpm; if the profile needs uncached packages, it can fail rather than download them. `--dump-config` shows configuration only; it does not confirm a live MCP connection or skill discovery. After starting DSH, check that `mcp__rolling-memory__recall` and `/rolling-memory-dsh` are available.

Configure the local vault and scope through Rolling Memory's own configuration or environment before starting DSH. The bundle sets the MCP process to local operation and disables its shared daemon so a previously running daemon cannot choose a different mode. Each MCP process initializes its own memory state, which can increase startup time and memory use. Database and scope settings are not replaced by the bundle; the MCP server may write recall-related state to that vault.

To remove the bundle, run `dsh plugin --profile web remove @rocareer/dsh-rolling-memory`. This removes the bundle from that profile; it does not delete the Rolling Memory vault or other profile entries.
