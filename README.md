# cub-helm

`cub-helm` is a [ConfigHub](https://confighub.com) CLI plugin that installs Helm
charts as ConfigHub components. It contributes the `cub helm` command group.

A chart is rendered entirely client-side and its output becomes units in the
component's base variant space (`<component>-base`). The chart reference,
values, and options are recorded as a `HelmSource` unit in the component's helm
source space (`<component>-helm`), which is the source of truth for upgrades.

Rendering never contacts a cluster: hooks are dropped unless `--include-hooks`
is set, the `lookup` template function returns nothing, and capabilities are
Helm's defaults. Charts that depend on those are out of scope; charts that
render cleanly client-side work fully.

Deployments are created from the base with `cub variant create` and updated
with `cub variant promote`; helm commands never touch them.

## Install

```
cub plugin install confighub/cub-helm
```

This downloads the latest release for your platform and registers the `helm`
command. Verify with:

```
cub helm version
```

## Commands

- `cub helm install <release-name> <chart-ref>` — render a chart and install it
  as a ConfigHub component (creates the `<component>-base` and `<component>-helm`
  spaces).
- `cub helm upgrade <release-name>` — re-render a release from its stored
  `HelmSource` and reconcile the base's units.
- `cub helm template <release-name> <chart-ref>` — render a chart locally and
  preview the units `cub helm install` would create, with no server connection.

Run any command with `--help` for its flags, and see the
[full guide](docs/guide.md) for the end-to-end workflow (values, namespaces,
CRDs, hooks, creating deployments, and upgrades).

```
# Install a chart as component "cubbychat"
cub helm install cubbychat oci://ghcr.io/confighub/charts/cubbychat

# Preview what install would create, offline
cub helm template cubbychat ./charts/cubbychat --output-dir ./out

# Upgrade the chart version; the change lands in the base
cub helm upgrade cubbychat --version 1.3.0
```

## Development

The plugin authenticates using the current `cub` session — it reads
`CUB_SERVER` and `CUB_TOKEN` from the environment, which `cub` sets when it
invokes a plugin. Run `cub auth login` first.

Build and test:

```
go build ./...
go test ./...
```

`test/smoke.sh` is an end-to-end test that installs, upgrades, and reconciles
the local fixture chart against a running ConfigHub server. It needs the plugin
installed and the current `cub` context pointed at a localhost server; run it
with `CUB=/path/to/cub test/smoke.sh`.

Install the locally-built binary as a plugin (overrides any released one):

```
go build -o ~/.confighub/plugins/helm .
cub helm version
```

Re-install the released version after local testing:

```
cub plugin install confighub/cub-helm --force
```

### SDK dependency

The chart-rendering logic lives in the ConfigHub SDK package
`github.com/confighub/sdk/bridge-impl/helmutils`. This repo pins
`github.com/confighub/sdk/bridge-impl` and `github.com/confighub/sdk/core` to a
published version (see `go.mod`); bump both together when picking up newer
`helmutils` changes. The two version-pin `replace` directives in `go.mod`
(a yaml CVE fix and the kustomize pin) mirror `bridge-impl`'s own pins, which do
not apply transitively to a downstream module.

## Releasing

Push a `v*` tag. The `release` workflow builds macOS/Linux (amd64/arm64)
binaries and attaches them to a GitHub release, which `cub plugin install`
consumes.

```
git tag v0.X.Y
git push origin v0.X.Y
```
