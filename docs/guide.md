# Using Helm Charts with ConfigHub

`cub helm` installs a Helm chart as a ConfigHub [component](https://docs.confighub.com/background/concepts/component/). The chart is rendered on your machine and the rendered output becomes the component's [base](https://docs.confighub.com/background/concepts/base/) — the shared configuration that [deployments](https://docs.confighub.com/background/concepts/variant/) are created from. From there you work with the chart's output the ConfigHub way: full revision history, review before apply, and changes that flow from the base out to each place the component runs.

`cub helm` is distributed as a ConfigHub CLI plugin. Install it once with:

```bash
cub plugin install confighub/cub-helm
```

## How it works

`cub helm install` renders the chart client-side, the same way `helm template` does — no Tiller, no Helm server, no cluster contact. What you see in ConfigHub is exactly the rendered Kubernetes YAML.

Because rendering never reaches a cluster, charts that depend on cluster access are out of scope: the `lookup` template function returns nothing, `.Capabilities` reflects Helm's defaults rather than a real cluster, and lifecycle hooks are dropped (see [Hooks and limitations](#hooks-and-limitations)). Simple, hook-free application charts — which is most of them — render fully. If a chart depends on those cluster-side behaviors for correctness, use a different delivery path.

## Install a chart

```bash
cub helm install <release-name> <chart-ref>
```

The chart reference may be an `oci://` reference, a local chart directory, or a chart name resolved against `--repo`. For example:

```bash
cub helm install cubbychat oci://ghcr.io/confighub/charts/cubbychat
```

This creates two [spaces](https://docs.confighub.com/background/entities/space/), grouped into the component by labels:

- `<component>-base` — the base: the rendered [units](https://docs.confighub.com/background/entities/unit/), one per resource. It has no [target](https://docs.confighub.com/background/entities/target/); it exists to create deployments from.
- `<component>-helm` — the record of where the base came from: a single `HelmSource` unit holding the chart reference, version, and values. Upgrades regenerate the base from this record.

The component defaults to the release name; pass `--component` to install into a named component (see [Multiple charts in one component](#multiple-charts-in-one-component)).

### What gets created

List the component's spaces:

```bash
cub space list --where "Labels.Component = 'cubbychat'"
```

The rendered chart is uploaded into the base, which holds one unit per resource:

```bash
cub unit list --space cubbychat-base
```

A workload's unit is named after the workload, and any other resource's unit after its name and kind. A leading `<component>-` is dropped from the name, so Helm's usual `<release>-<name>` names read as their short form when the release and the component share a name:

| Resource | Unit slug |
| --- | --- |
| Deployment `cubbychat-backend` | `backend` |
| Service `cubbychat-backend` | `backend-service` |
| Role `cubbychat-reader` | `reader-role` |
| CustomResourceDefinition `widgets.example.com` | `widgets.example.com-crd` |

Each unit is ordinary Kubernetes YAML. It records the chart template it came from in its `UploadFile` annotation, such as `cubbychat/charts/postgres/templates/statefulset.yaml`, and carries labels (`HelmChart`, `HelmRelease`, chart version, app version) tracing it back to the chart. Inspect one:

```bash
cub unit data --space cubbychat-base backend
```

## Values

Supply values with `--values`/`-f` files and `--set` overrides, exactly as with Helm. Later files override earlier ones, and `--set` overrides files:

```bash
cub helm install myapp bitnami/nginx \
  --repo https://charts.bitnami.com/bitnami \
  --values base-values.yaml \
  --values prod-values.yaml \
  --set replicaCount=5
```

The resolved values are stored in the `HelmSource` unit. They are the source of truth for upgrades — you never re-supply them on the command line unless you mean to change them.

## Namespaces

Specifying the namespace and creating the Namespace resource are separate concerns, as they are in Helm itself.

The chart is rendered into its release namespace, `--namespace`, which defaults to the release name. The base carries that real namespace rather than a placeholder: charts write `.Release.Namespace` into places that `set-namespace` cannot rewrite, such as ConfigMap data, command-line flags, and webhook service references, so a namespace substituted after rendering would be missed in some of them. The namespace is recorded in the base's `Namespace` label.

Resources the chart places in other namespaces, such as a RoleBinding in `kube-system`, stay in the same base with their namespaces as rendered.

`--create-namespace` synthesizes a Namespace unit for the release namespace, mirroring `helm install --create-namespace`. It is skipped automatically when the chart already renders its own Namespace, so you never get a duplicate. Without it, the namespace is assumed to be managed elsewhere, and the install output lists the references to it that nothing in the chart provides.

## CRDs

CRDs shipped in a chart's `crds/` directory become units like any other resource, named `<name>-crd`. `--skip-crds` omits them, mirroring `helm install --skip-crds`; it does not affect CRDs emitted from `templates/`.

## Hooks and limitations

Client-side rendering emits `helm.sh/hook`-annotated manifests as if they were plain resources, but without Helm's lifecycle they would simply sit in the cluster. `cub helm` drops hook manifests by default and reports what it dropped:

```
Dropped hook manifest: Job db-init (helm.sh/hook: pre-install) from mychart/templates/hook.yaml
```

`--include-hooks` keeps them as plain resources for the occasional chart where the hook objects are harmless or wanted. There is no way to run Helm's hook lifecycle from ConfigHub — that is a deliberate consequence of rendering without a cluster.

The same boundary applies to `lookup` (returns empty) and `.Capabilities` (Helm defaults). A chart that relies on any of these for correct output is out of scope for `cub helm`.

## Create a deployment

The base is not deployed directly. A deployment is a [variant](https://docs.confighub.com/background/concepts/variant/) of the component, created from the base and pointed at a target:

```bash
cub variant create dev cubbychat-base \
    --target dev-cluster/oci
```

`--target` binds the deployment's units for release, and the deployment runs in the base's namespace. `cub variant create --namespace` can move it to another, but it rewrites only resource namespaces and the references to them that it knows, not the places a chart may also have written `.Release.Namespace`. To run a chart in another namespace, install it under another component with that `--namespace`. See [Creating and managing variants](https://docs.confighub.com/guide/variants/) for the full workflow, including promotion and release.

## Customize

Do not edit the units in the base directly to customize a single environment — the base is shared by every deployment. Instead:

- To change chart values or the chart version for everyone, run `cub helm upgrade` (below). The change lands in the base and flows to deployments through promotion.
- To change how the rendered configuration behaves for everyone, edit the base units — with `cub unit edit` or a [function](https://docs.confighub.com/guide/functions/) — then promote.
- To change one deployment only, edit that deployment's units. ConfigHub records the change as owned by that unit, so a later promotion from the base will not clobber it.

Changes reach deployments only when you promote:

```bash
cub variant promote cubbychat-dev --dry-run -o mutations   # preview
cub variant promote cubbychat-dev
```

## Upgrade

`cub helm upgrade` targets the base, not the deployments. Flags patch the stored `HelmSource` first, then the chart is re-rendered from that record alone — you do not repeat the chart reference or the values:

```bash
# Upgrade to a new chart version
cub helm upgrade cubbychat --version 1.3.0

# Change values (replaces the stored values)
cub helm upgrade cubbychat --set ai.enabled=true

# Re-render after editing the HelmSource unit by hand
cub helm upgrade cubbychat
```

The re-rendered chart is uploaded into the base. A resource that changed is merged into its unit, so an edit made to the base in ConfigHub is kept unless the chart changed the same field. A new resource gets a new unit. A resource the chart no longer renders has its unit emptied, not deleted: the unit keeps its history and links, the next release withdraws the object, and the unit is filled again if the resource returns. Preview an upgrade with `--dry-run`. The change stops at the base; promote it into each deployment when you are ready:

```bash
cub variant promote cubbychat-dev
```

Because the base and its deployments are versioned units, every upgrade is a reviewable revision. You can diff before promoting:

```bash
cub unit diff backend --space cubbychat-base --from 1
```

## Multiple charts in one component

A component can be built from more than one chart. Install additional releases into the same component with `--component`:

```bash
cub helm install cubbychat oci://ghcr.io/confighub/charts/cubbychat
cub helm install --component cubbychat --namespace cubbychat \
    pg oci://registry-1.docker.io/bitnamicharts/postgresql
```

Each release's units are named from its own resources, which charts usually name after the release (`pg-postgresql`). When two releases do render a resource with the same name, such as a CRD both charts ship, the later one's unit gets a numbered slug. Both units define the same object, so decide which release should own it. Each release owns the units it wrote, so upgrading one release never updates or empties another's.

## Preview without installing

`cub helm template` renders a chart and shows what `cub helm install` would do with it: the units it would create, and on a component that already has the release, those it would update or empty. It writes nothing, and takes the same flags as install; it is the same as `cub helm install --dry-run`. The server decides the units, so it needs a server connection.

```bash
cub helm template cubbychat oci://ghcr.io/confighub/charts/cubbychat
```

To see the rendered YAML itself, use `helm template`.

## Troubleshooting

Chart not found. For a chart name resolved against a repository, make sure the repository is reachable and the name and version are correct:

```bash
cub helm install myapp myrepo/mychart --repo https://example.com/charts --version 1.2.3
```

`oci://` references and local chart directories are resolved directly.

Missing resources after install. If a resource you expected is absent, it was likely a dropped hook manifest — check the install output for `Dropped hook manifest` lines, and pass `--include-hooks` if you want those objects kept as plain resources. If a resource renders only when the chart talks to a cluster (via `lookup` or `.Capabilities`), it will not appear; such charts are out of scope.

## Summary

`cub helm` brings the Helm ecosystem into the component/variant model: a chart is rendered client-side into a component's base, deployments are created from that base with `cub variant create`, and chart upgrades flow from the base to each deployment through `cub variant promote`. You get Helm's charts and values together with ConfigHub's revision history, review, and safe promotion — as long as the chart renders without a cluster.
