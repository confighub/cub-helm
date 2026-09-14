#!/bin/bash -x
set -e

# End-to-end smoke test for the cub helm plugin in the component/variant model.
#
# `cub helm install` renders a chart client-side and installs it as a component:
#   - a helm source space  <component>-helm  holding one HelmSource unit per release
#   - a base variant space <component>-base  holding one unit per rendered resource,
#     written by uploading the rendered chart
# The source space is annotated with GeneratesSpaceID pointing at the base space.
# `cub helm upgrade` patches the HelmSource and uploads the re-rendered chart, which
# merges changed resources and empties the units of resources no longer rendered.
#
# Uses a local chart fixture (test/testdata/smoke) so it needs no Helm
# repositories and no cluster: install/upgrade only write units.
#
# Prerequisites:
#   - a running ConfigHub server, with the current cub context pointing at it
#     and authenticated (`cub auth login`)
#   - the plugin installed so `cub helm` resolves to it, either via
#     `cub plugin install confighub/cub-helm` or, for local development,
#     `go build -o ~/.confighub/plugins/helm .`
#
# Override the cub binary with CUB=/path/to/cub. Set NOCLEANUP=1 to keep the
# spaces this test creates.

cub="${CUB:-cub}"
SCRIPTDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART="$SCRIPTDIR/testdata/smoke"

# Safety: only run against a localhost server, since the test creates and
# deletes spaces.
server_url=$("$cub" context get -o jq=.coordinate.serverURL 2>/dev/null || true)
if [[ ! "$server_url" =~ ^https?://(localhost|127\.0\.0\.1) ]] ; then
    echo "ERROR: cub context server is '${server_url}', not localhost." >&2
    echo "ERROR: Aborting to avoid running the smoke test against a non-local server." >&2
    echo "ERROR: Switch with 'cub context use <localhost-context>' first." >&2
    exit 1
fi

# --- Minimal assertion helpers (ported from the monorepo test-lib) ---

function verifyEntityExists {
    local space="$1" entityType="$2" entityName="$3"
    local spaceFlag=""
    [[ -n "$space" ]] && spaceFlag="--space $space"
    if ! $cub "$entityType" list $spaceFlag --no-headers -o name | grep -qxE "([^/]*/)?$entityName" ; then
        echo "$entityName of type $entityType not found in list" >&2
        exit 1
    fi
}

function verifyEntityDoesNotExist {
    local space="$1" entityType="$2" entityName="$3"
    local spaceFlag=""
    [[ -n "$space" ]] && spaceFlag="--space $space"
    if $cub "$entityType" list $spaceFlag --no-headers -o name | grep -qxE "([^/]*/)?$entityName" ; then
        echo "$entityName of type $entityType unexpectedly found in list" >&2
        exit 1
    fi
}

function checkSpaceMetadataValue {
    local space="$1" field="$2" value="$3"
    local result
    result=$($cub space get -o jq="$field" "$space")
    if [[ "$result" != "$value" ]] ; then
        echo "space $space $field: $result != $value" >&2
        exit 1
    fi
}

function checkUnitConfigValue {
    local space="$1" unit="$2" field="$3" value="$4"
    local result
    result=$($cub function do --space "$space" --unit "$unit" --show output yq "$field")
    if [[ "$result" != "$value" ]] ; then
        echo "unit $unit $field: $result != $value" >&2
        exit 1
    fi
}

function checkUnitMetadataValue {
    local space="$1" unit="$2" field="$3" value="$4"
    local result
    result=$($cub unit get --space "$space" -o jq="$field" "$unit")
    if [[ "$result" != "$value" ]] ; then
        echo "unit $unit $field: $result != $value" >&2
        exit 1
    fi
}

function checkUnitDataEmpty {
    local space="$1" unit="$2"
    local result
    result=$($cub unit data --space "$space" "$unit")
    if [[ -n "$result" ]] ; then
        echo "unit $unit is not empty: $result" >&2
        exit 1
    fi
}

function expectError {
    local output="$1" expected_msg="$2"
    if ! echo -n "$output" | grep -zq "$expected_msg"; then
        echo "Expected error message not found:" >&2
        echo "Expected: $expected_msg" >&2
        echo "Got: $output" >&2
        exit 1
    fi
}

# --- Test ---

COMP="helmsmoke$RANDOM"
BASE="$COMP-base"
SOURCE="$COMP-helm"

function helmCleanup {
    $cub space delete --recursive "$BASE" || true
    $cub space delete --recursive "$SOURCE" || true
    $cub space delete --recursive "${COMP}ns-base" || true
    $cub space delete --recursive "${COMP}ns-helm" || true
}
if [[ -z "$NOCLEANUP" ]] ; then
    trap helmCleanup SIGINT SIGTERM SIGHUP EXIT
fi

### Install
echo "Test 1: install the smoke chart as component $COMP"
$cub helm install \
  --component "$COMP" \
  --namespace smoke \
  --create-namespace \
  --set replicas=2 \
  --set extra.enabled=true \
  web \
  "$CHART"

# Both spaces exist, stamped with the expected labels.
verifyEntityExists "" space "$BASE"
verifyEntityExists "" space "$SOURCE"
checkSpaceMetadataValue "$BASE" '.Space.Labels.Component' "$COMP"
checkSpaceMetadataValue "$BASE" '.Space.Labels.Variant' "base"
checkSpaceMetadataValue "$SOURCE" '.Space.Labels.Variant' "helm-source"

# The source space's generator annotation points at the base space.
baseID=$($cub space get "$BASE" -o jq='.Space.SpaceID')
checkSpaceMetadataValue "$SOURCE" '.Space.Annotations.GeneratesSpaceID' "$baseID"

# One unit per resource: a workload is named after itself, anything else after
# its name and kind. Each records the chart template it came from.
verifyEntityExists "$BASE" unit web
verifyEntityExists "$BASE" unit web-service
verifyEntityExists "$BASE" unit web-extra-configmap
verifyEntityExists "$BASE" unit widgets.example.com-crd
verifyEntityExists "$BASE" unit namespace
checkUnitMetadataValue "$BASE" web '.Unit.Annotations.UploadFile' "smoke/templates/deployment.yaml"
checkUnitMetadataValue "$BASE" widgets.example.com-crd '.Unit.Annotations.UploadFile' "smoke/crds/widget.yaml"
checkUnitMetadataValue "$BASE" web '.Unit.Labels.UploadSource' "web"
checkUnitMetadataValue "$BASE" web '.Unit.Labels.HelmChart' "smoke"
checkSpaceMetadataValue "$BASE" '.Space.Labels.Namespace' "smoke"

# Hook manifests are dropped by default, so no unit is generated from hook.yaml.
verifyEntityDoesNotExist "$BASE" unit web-hook

# The HelmSource unit lives in the source space.
verifyEntityExists "$SOURCE" unit web

# Values and the specified namespace are rendered into the base units.
checkUnitConfigValue "$BASE" web '.spec.replicas' "2"
checkUnitConfigValue "$BASE" web '.metadata.namespace' "smoke"

### Upgrade
echo "Test 2: upgrade changes values and reconciles the base units"
# upgrade re-renders from the stored HelmSource, so it takes only the release
# name (no chart ref).
$cub helm upgrade \
  --component "$COMP" \
  --set replicas=3 \
  --set extra.enabled=false \
  web

# The changed value flows into the existing unit.
checkUnitConfigValue "$BASE" web '.spec.replicas' "3"

# extra.enabled=false makes extra-configmap.yaml render empty, so its unit is
# emptied: it keeps its history, and returns if the resource does.
verifyEntityExists "$BASE" unit web-extra-configmap
checkUnitDataEmpty "$BASE" web-extra-configmap

### A dry run writes nothing
echo "Test 2b: upgrade --dry-run reports the plan and writes nothing"
OUTPUT=$($cub helm upgrade --component "$COMP" --dry-run --set replicas=4 web)
expectError "$OUTPUT" "Update    web"
checkUnitConfigValue "$BASE" web '.spec.replicas' "3"

### Without --namespace the release name is the namespace
echo "Test 2c: the release namespace defaults to the release name"
$cub helm install --component "${COMP}ns" noname "$CHART"
checkUnitConfigValue "${COMP}ns-base" noname '.metadata.namespace' "noname"
checkSpaceMetadataValue "${COMP}ns-base" '.Space.Labels.Namespace' "noname"

### Second release in the same component, with a unit prefix
echo "Test 3: a second release adds prefixed units to the same component"
$cub helm install \
  --component "$COMP" \
  --namespace smoke \
  --prefix pg \
  pg \
  "$CHART"

verifyEntityExists "$BASE" unit pg-pg
verifyEntityExists "$BASE" unit pg-pg-service
verifyEntityExists "$SOURCE" unit pg
# Each release owns its own units, so installing pg left web's alone.
checkUnitMetadataValue "$BASE" pg-pg '.Unit.Labels.UploadSource' "pg"
checkUnitConfigValue "$BASE" web '.spec.replicas' "3"

### Empty prefix may be used by at most one release in a component
echo "Test 4: a second empty-prefix release is rejected"
OUTPUT=$($cub helm install \
  --component "$COMP" \
  --prefix "" \
  dup \
  "$CHART" 2>&1 || true)
expectError "$OUTPUT" "already uses an empty unit prefix"

echo "cub helm smoke test passed"
