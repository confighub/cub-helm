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

### `cub helm install <release-name> <chart-ref>`

Render a Helm chart and install it as a ConfigHub component. The chart reference
may be an `oci://` reference, a local chart directory, or a chart name resolved
against `--repo`. Creates the `<component>-base` and `<component>-helm` spaces if
missing (component defaults to the release name).

```
# Install a chart as component "cubbychat"
cub helm install cubbychat oci://ghcr.io/confighub/charts/cubbychat

# Add a second chart to the same component; its units are prefixed "pg-"
cub helm install --component cubbychat --prefix pg pg oci://registry-1.docker.io/bitnamicharts/postgresql

# Explicit namespace and a synthesized Namespace unit
cub helm install --namespace cert-manager --create-namespace cert-manager jetstack/cert-manager --version v1.17.1
```

### `cub helm upgrade <release-name>`

Re-render a release from its `HelmSource` unit and reconcile the base space's
units. Flags patch the `HelmSource` first (`--version` replaces the chart
version constraint; `-f`/`--set` replace the stored values). With no flags,
upgrade is a plain re-render — the way a hand-edit of the `HelmSource` unit is
applied.

```
# Upgrade to a new chart version
cub helm upgrade cubbychat --version 1.3.0

# Change values (replaces the stored values)
cub helm upgrade cubbychat -f values.yaml --set ai.enabled=true

# Re-render after editing the HelmSource unit by hand
cub helm upgrade cubbychat
```

### `cub helm template <release-name> <chart-ref>`

Render a chart locally and show the units `cub helm install` would generate,
without a server connection. Writes to stdout by default, or one `<slug>.yaml`
file per unit with `--output-dir`.

```
cub helm template cubbychat oci://ghcr.io/confighub/charts/cubbychat
cub helm template cubbychat ./charts/cubbychat --output-dir ./out
```

## How it maps to ConfigHub

Rendered output becomes one unit per chart template file, named from the chart's
file layout: `templates/backend.yaml` becomes unit `backend`,
`templates/rbac/role.yaml` becomes `rbac-role`, `crds/foo.yaml` becomes
`crds-foo`, and subchart files are prefixed with the subchart name. When a
component contains multiple releases, each release's units are namespaced with
`--prefix` (defaulted to the release name for the second and later releases).

The base is untargeted. If `--namespace` is not given, the release renders with
the `confighubplaceholder` namespace, which each deployment fills:

```
cub variant create <variant> <component>-base --target <space>/<target> --namespace <ns>
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
